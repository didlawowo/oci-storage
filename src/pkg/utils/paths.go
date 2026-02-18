// pkg/utils/paths.go
// PathManager is a pure path calculator. It returns relative paths for storage
// operations (to be resolved by the storage Backend) and absolute paths only
// for temp files (which are always local).
package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PathManager struct {
	baseStoragePath string
}

// NewPathManager creates a path manager. The basePath is only used for
// temp file paths and GetBasePath() (backward compat for backup service).
// Storage paths are returned as relative paths for the Backend to resolve.
func NewPathManager(basePath string, _ *Logger) *PathManager {
	// Only create temp directory (always local, never in Backend)
	_ = os.MkdirAll(filepath.Join(basePath, "temp"), 0755)

	return &PathManager{
		baseStoragePath: basePath,
	}
}

// GetTempPath returns an ABSOLUTE path for temp files.
// Temp files are always local (chunked uploads need local staging).
func (pm *PathManager) GetTempPath(uuid string) string {
	return filepath.Join(pm.baseStoragePath, "temp", uuid)
}

// GetBlobPath returns a relative storage path for a blob.
func (pm *PathManager) GetBlobPath(digest string) string {
	return filepath.Join("blobs", digest)
}

// GetManifestPath returns a relative storage path for a manifest.
func (pm *PathManager) GetManifestPath(name, reference string) string {
	return filepath.Join("manifests", name, reference+".json")
}

// GetChartPath returns a relative storage path for a chart archive.
func (pm *PathManager) GetChartPath(chartName, version string) string {
	return filepath.Join("charts", fmt.Sprintf("%s-%s.tgz", chartName, version))
}

// GetBasePath returns the absolute base storage path.
// Used by backup service and scan service for direct filesystem access.
func (pm *PathManager) GetBasePath() string {
	return pm.baseStoragePath
}

// GetChartsPath returns the relative charts directory path.
func (pm *PathManager) GetChartsPath() string {
	return "charts"
}

// GetIndexPath returns the relative path for index.yaml.
func (pm *PathManager) GetIndexPath() string {
	return "index.yaml"
}

// GetImageManifestPath returns a relative storage path for an image manifest.
func (pm *PathManager) GetImageManifestPath(name, reference string) string {
	safeRef := strings.ReplaceAll(reference, ":", "_")
	return filepath.Join("images", name, "manifests", safeRef+".json")
}

// GetCacheStatePath returns the relative path for the cache state file.
func (pm *PathManager) GetCacheStatePath() string {
	return filepath.Join("cache", "state.json")
}

// GetCachedImageMetadataPath returns the relative path for cached image metadata.
func (pm *PathManager) GetCachedImageMetadataPath(name, tag string) string {
	safeName := strings.ReplaceAll(name, "/", "_")
	return filepath.Join("cache", "metadata", safeName+"_"+tag+".json")
}
