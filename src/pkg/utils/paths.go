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
	"syscall"
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

// DiskStats contains filesystem usage information for the storage volume.
type DiskStats struct {
	Total     int64 `json:"total"`     // Total capacity in bytes
	Used      int64 `json:"used"`      // Used space in bytes
	Available int64 `json:"available"` // Available space in bytes
}

// GetDiskStats returns the actual filesystem capacity and usage for the storage path.
// This reads the real PVC/disk size instead of relying on hardcoded config values.
func (pm *PathManager) GetDiskStats() (*DiskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(pm.baseStoragePath, &stat); err != nil {
		return nil, fmt.Errorf("failed to stat filesystem at %s: %w", pm.baseStoragePath, err)
	}
	total := int64(stat.Blocks) * int64(stat.Bsize)
	available := int64(stat.Bavail) * int64(stat.Bsize)
	used := total - available
	return &DiskStats{
		Total:     total,
		Used:      used,
		Available: available,
	}, nil
}
