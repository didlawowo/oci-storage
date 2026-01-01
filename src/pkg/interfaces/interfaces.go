package interfaces

import (
	"context"
	"io"

	"oci-storage/pkg/models"
	storage "oci-storage/pkg/utils"
)

type ChartServiceInterface interface {
	SaveChart(data []byte, filename string) error
	ListCharts() ([]models.ChartGroup, error)
	ChartExists(name, version string) bool
	GetChart(name, version string) ([]byte, error)
	GetChartDetails(name, version string) (*models.ChartMetadata, error)
	DeleteChart(name, version string) error
	GetPathManager() *storage.PathManager
	GetChartValues(name, version string) (string, error)
	ExtractChartMetadata(chartData []byte) (*models.ChartMetadata, error)
}

type ImageServiceInterface interface {
	// SaveImage saves a Docker image manifest and metadata
	SaveImage(name, reference string, manifest *models.OCIManifest) error
	// ListImages returns all available images grouped by name
	ListImages() ([]models.ImageGroup, error)
	// ImageExists checks if an image with the given name and tag exists
	ImageExists(name, tag string) bool
	// GetImageManifest returns the manifest for a specific image
	GetImageManifest(name, reference string) (*models.OCIManifest, error)
	// GetImageMetadata returns metadata for a specific image
	GetImageMetadata(name, tag string) (*models.ImageMetadata, error)
	// DeleteImage removes an image by name and tag
	DeleteImage(name, tag string) error
	// GetImageConfig returns the parsed image configuration
	GetImageConfig(name, tag string) (*models.ImageConfig, error)
	// ListTags returns all tags for a given repository
	ListTags(name string) ([]string, error)
	// GetPathManager returns the path manager
	GetPathManager() *storage.PathManager
}

type BackupServiceInterface interface {
	BackupCharts() error
	RestoreCharts() error
}

type IndexServiceInterface interface {
	UpdateIndex() error
	GetIndexPath() string
	EnsureIndexExists() error
}

// ProxyServiceInterface handles Docker registry proxying and caching
type ProxyServiceInterface interface {
	// ResolveRegistry parses an image path and determines the upstream registry
	ResolveRegistry(imagePath string) (registryURL string, imageName string, err error)
	// GetDefaultRegistry returns the default upstream registry URL
	GetDefaultRegistry() string
	// GetManifest fetches a manifest from upstream registry
	GetManifest(ctx context.Context, registryURL, name, reference string) ([]byte, string, error)
	// GetBlob fetches a blob from upstream registry
	GetBlob(ctx context.Context, registryURL, name, digest string) (io.ReadCloser, int64, error)
	// GetCacheState returns the current cache state
	GetCacheState() *models.CacheState
	// GetCachedImages returns all cached images metadata
	GetCachedImages() ([]models.CachedImageMetadata, error)
	// UpdateAccessTime updates the last accessed time for a cached image
	UpdateAccessTime(name, tag string)
	// EvictLRU removes least recently used images until target size is reached
	EvictLRU(targetBytes int64) error
	// DeleteCachedImage removes a specific cached image
	DeleteCachedImage(name, tag string) error
	// AddToCache adds image metadata to the cache tracking
	AddToCache(metadata models.CachedImageMetadata) error
	// IsEnabled returns whether the proxy is enabled
	IsEnabled() bool
}
