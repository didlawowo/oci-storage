// pkg/storage/backend.go
// Storage backend abstraction for local filesystem and S3-compatible object storage.
// When S3 is enabled, blobs/manifests/charts are stored in the bucket instead of local disk,
// allowing horizontal scaling with multiple replicas.
package storage

import (
	"io"
)

// FileInfo holds metadata about a stored object
type FileInfo struct {
	Name string
	Size int64
	IsDir bool
}

// Backend abstracts file I/O operations so that the rest of the application
// does not depend on a local filesystem. Implementations exist for local disk
// and S3-compatible object stores (Garage, MinIO, AWS S3).
type Backend interface {
	// Read returns the full content of a file/object
	Read(path string) ([]byte, error)

	// Write stores data at the given path (creates parent dirs/prefixes as needed)
	Write(path string, data []byte) error

	// WriteStream stores data from a reader at the given path
	WriteStream(path string, reader io.Reader) (int64, error)

	// Exists returns true if the file/object exists
	Exists(path string) (bool, error)

	// Stat returns metadata about a file/object (size, name)
	Stat(path string) (*FileInfo, error)

	// Delete removes a file/object
	Delete(path string) error

	// List returns entries in a directory/prefix (non-recursive)
	List(dir string) ([]FileInfo, error)

	// ReadStream returns a reader for streaming large files/objects
	ReadStream(path string) (io.ReadCloser, error)

	// Rename atomically moves a file/object from src to dst
	// On S3 this is copy+delete (not truly atomic but sufficient)
	Rename(src, dst string) error

	// CreateTemp creates a temporary file and returns a writer.
	// The caller must call Close() and then use Rename() to move it.
	// On S3 backend, this falls back to local temp then upload on Rename.
	CreateTemp(dir string) (TempFile, error)

	// Import moves a local file into storage. localPath is an absolute
	// filesystem path (e.g. a temp upload file). storagePath is a relative
	// path within the backend.
	// For local backend this is an efficient os.Rename.
	// For S3 backend this uploads the file then deletes the local copy.
	Import(localPath, storagePath string) error

	// RemoveAll removes a directory/prefix and all its contents.
	RemoveAll(path string) error
}

// TempFile wraps a temporary file for chunked uploads.
// Write data to it, Close it, then call Path() to get the location.
type TempFile interface {
	io.Writer
	Close() error
	Path() string
}
