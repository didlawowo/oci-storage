package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/config"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

// S3Backend implements Backend using S3-compatible object storage.
// Compatible with AWS S3, Garage, and MinIO.
type S3Backend struct {
	client *s3.S3
	bucket string
	// localTempDir is used for CreateTemp (chunked uploads need local staging)
	localTempDir string
}

// NewS3Backend creates an S3 storage backend from config
func NewS3Backend(cfg config.S3Config, localTempDir string) (*S3Backend, error) {
	sess, err := session.NewSession(&aws.Config{
		Endpoint:         aws.String(cfg.Endpoint),
		Region:           aws.String(cfg.Region),
		Credentials:      credentials.NewStaticCredentials(cfg.AccessKey, cfg.SecretKey, ""),
		S3ForcePathStyle: aws.Bool(cfg.PathStyle),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 session: %w", err)
	}

	client := s3.New(sess)

	// Verify bucket exists (or create it)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = client.HeadBucketWithContext(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(cfg.Bucket),
	})
	if err != nil {
		// Try to create the bucket
		_, createErr := client.CreateBucketWithContext(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(cfg.Bucket),
		})
		if createErr != nil {
			return nil, fmt.Errorf("bucket %s does not exist and could not be created: %w (original: %v)", cfg.Bucket, createErr, err)
		}
	}

	// Ensure local temp dir exists for chunked uploads
	if err := os.MkdirAll(localTempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	return &S3Backend{
		client:       client,
		bucket:       cfg.Bucket,
		localTempDir: localTempDir,
	}, nil
}

func (b *S3Backend) key(path string) string {
	// Normalize path separators and remove leading slash
	k := strings.ReplaceAll(path, string(filepath.Separator), "/")
	k = strings.TrimPrefix(k, "/")
	return k
}

func (b *S3Backend) Read(path string) ([]byte, error) {
	out, err := b.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (b *S3Backend) Write(path string, data []byte) error {
	_, err := b.client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (b *S3Backend) WriteStream(path string, reader io.Reader) (int64, error) {
	// Buffer to a temp file first, then upload (needed for Content-Length)
	tmp, err := os.CreateTemp(b.localTempDir, ".s3upload-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmp, reader)
	if err != nil {
		tmp.Close()
		return written, err
	}
	tmp.Seek(0, io.SeekStart)

	_, err = b.client.PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(b.bucket),
		Key:           aws.String(b.key(path)),
		Body:          tmp,
		ContentLength: aws.Int64(written),
	})
	tmp.Close()
	return written, err
}

func (b *S3Backend) Exists(path string) (bool, error) {
	_, err := b.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *S3Backend) Stat(path string) (*FileInfo, error) {
	out, err := b.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
	})
	if err != nil {
		return nil, err
	}

	k := b.key(path)
	parts := strings.Split(k, "/")
	name := parts[len(parts)-1]

	return &FileInfo{
		Name:  name,
		Size:  aws.Int64Value(out.ContentLength),
		IsDir: false,
	}, nil
}

func (b *S3Backend) Delete(path string) error {
	_, err := b.client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
	})
	return err
}

func (b *S3Backend) List(dir string) ([]FileInfo, error) {
	prefix := b.key(dir)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	out, err := b.client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, err
	}

	var result []FileInfo

	// Common prefixes = "directories"
	for _, p := range out.CommonPrefixes {
		name := strings.TrimPrefix(aws.StringValue(p.Prefix), prefix)
		name = strings.TrimSuffix(name, "/")
		if name != "" {
			result = append(result, FileInfo{Name: name, IsDir: true})
		}
	}

	// Objects = "files"
	for _, obj := range out.Contents {
		name := strings.TrimPrefix(aws.StringValue(obj.Key), prefix)
		if name != "" && name != "/" {
			result = append(result, FileInfo{
				Name: name,
				Size: aws.Int64Value(obj.Size),
			})
		}
	}

	return result, nil
}

func (b *S3Backend) ReadStream(path string) (io.ReadCloser, error) {
	out, err := b.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(path)),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (b *S3Backend) Rename(src, dst string) error {
	// S3 has no rename - copy then delete
	_, err := b.client.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(b.bucket),
		CopySource: aws.String(b.bucket + "/" + b.key(src)),
		Key:        aws.String(b.key(dst)),
	})
	if err != nil {
		return fmt.Errorf("S3 copy failed: %w", err)
	}

	_, err = b.client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.key(src)),
	})
	return err
}

type s3TempFile struct {
	file    *os.File
	backend *S3Backend
}

func (f *s3TempFile) Write(p []byte) (int, error) { return f.file.Write(p) }
func (f *s3TempFile) Close() error                 { return f.file.Close() }
func (f *s3TempFile) Path() string                 { return f.file.Name() }

func (b *S3Backend) CreateTemp(dir string) (TempFile, error) {
	// Chunked uploads stage locally, then get uploaded to S3 on CompleteUpload
	f, err := os.CreateTemp(b.localTempDir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	return &s3TempFile{file: f, backend: b}, nil
}

func (b *S3Backend) Import(localPath, storagePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file for import: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}

	_, err = b.client.PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(b.bucket),
		Key:           aws.String(b.key(storagePath)),
		Body:          file,
		ContentLength: aws.Int64(info.Size()),
	})
	if err != nil {
		return fmt.Errorf("S3 upload failed: %w", err)
	}

	return os.Remove(localPath)
}

func (b *S3Backend) RemoveAll(path string) error {
	prefix := b.key(path)
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	// List all objects under prefix and delete them
	listInput := &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(prefix),
	}

	for {
		out, err := b.client.ListObjectsV2(listInput)
		if err != nil {
			return fmt.Errorf("failed to list objects for removal: %w", err)
		}

		for _, obj := range out.Contents {
			_, err := b.client.DeleteObject(&s3.DeleteObjectInput{
				Bucket: aws.String(b.bucket),
				Key:    obj.Key,
			})
			if err != nil {
				return fmt.Errorf("failed to delete object %s: %w", aws.StringValue(obj.Key), err)
			}
		}

		if !aws.BoolValue(out.IsTruncated) {
			break
		}
		listInput.ContinuationToken = out.NextContinuationToken
	}

	return nil
}

// isS3NotFound checks if an S3 error is a 404/NoSuchKey
func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "NotFound") ||
		strings.Contains(errStr, "NoSuchKey") ||
		strings.Contains(errStr, "404")
}
