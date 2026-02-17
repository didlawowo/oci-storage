package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// LocalBackend implements Backend using the local filesystem.
// This is the default storage backend when S3 is not enabled.
type LocalBackend struct {
	basePath string
}

// NewLocalBackend creates a local filesystem storage backend
func NewLocalBackend(basePath string) *LocalBackend {
	return &LocalBackend{basePath: basePath}
}

func (b *LocalBackend) resolve(path string) string {
	return filepath.Join(b.basePath, path)
}

func (b *LocalBackend) Read(path string) ([]byte, error) {
	return os.ReadFile(b.resolve(path))
}

func (b *LocalBackend) Write(path string, data []byte) error {
	full := b.resolve(path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}

	// Atomic write: temp file + rename
	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, full)
}

func (b *LocalBackend) WriteStream(path string, reader io.Reader) (int64, error) {
	full := b.resolve(path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return 0, err
	}

	dir := filepath.Dir(full)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()

	written, err := io.Copy(tmp, reader)
	tmp.Close()
	if err != nil {
		os.Remove(tmpPath)
		return written, err
	}
	if err := os.Rename(tmpPath, full); err != nil {
		os.Remove(tmpPath)
		return written, err
	}
	return written, nil
}

func (b *LocalBackend) Exists(path string) (bool, error) {
	_, err := os.Stat(b.resolve(path))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (b *LocalBackend) Stat(path string) (*FileInfo, error) {
	info, err := os.Stat(b.resolve(path))
	if err != nil {
		return nil, err
	}
	return &FileInfo{
		Name:  info.Name(),
		Size:  info.Size(),
		IsDir: info.IsDir(),
	}, nil
}

func (b *LocalBackend) Delete(path string) error {
	return os.Remove(b.resolve(path))
}

func (b *LocalBackend) List(dir string) ([]FileInfo, error) {
	entries, err := os.ReadDir(b.resolve(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	result := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, FileInfo{
			Name:  e.Name(),
			Size:  info.Size(),
			IsDir: e.IsDir(),
		})
	}
	return result, nil
}

func (b *LocalBackend) ReadStream(path string) (io.ReadCloser, error) {
	return os.Open(b.resolve(path))
}

func (b *LocalBackend) Rename(src, dst string) error {
	fullDst := b.resolve(dst)
	if err := os.MkdirAll(filepath.Dir(fullDst), 0755); err != nil {
		return err
	}
	return os.Rename(b.resolve(src), fullDst)
}

type localTempFile struct {
	file *os.File
}

func (f *localTempFile) Write(p []byte) (int, error) { return f.file.Write(p) }
func (f *localTempFile) Close() error                 { return f.file.Close() }
func (f *localTempFile) Path() string                 { return f.file.Name() }

func (b *LocalBackend) CreateTemp(dir string) (TempFile, error) {
	full := b.resolve(dir)
	if err := os.MkdirAll(full, 0755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(full, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	return &localTempFile{file: f}, nil
}

func (b *LocalBackend) Import(localPath, storagePath string) error {
	fullDst := b.resolve(storagePath)
	if err := os.MkdirAll(filepath.Dir(fullDst), 0755); err != nil {
		return err
	}
	return os.Rename(localPath, fullDst)
}

func (b *LocalBackend) RemoveAll(path string) error {
	return os.RemoveAll(b.resolve(path))
}
