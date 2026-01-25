// pkg/services/gc.go
package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// GCResult contains the results of a garbage collection run
type GCResult struct {
	OrphanBlobsDeleted   int   `json:"orphanBlobsDeleted"`
	OrphanBlobsBytes     int64 `json:"orphanBlobsBytes"`
	StaleImagesDeleted   int   `json:"staleImagesDeleted"`
	StaleImagesBytes     int64 `json:"staleImagesBytes"`
	TotalBytesReclaimed  int64 `json:"totalBytesReclaimed"`
	DurationMs           int64 `json:"durationMs"`
	Errors               []string `json:"errors,omitempty"`
}

// GCService handles garbage collection for orphan blobs and stale images
type GCService struct {
	config       *config.Config
	pathManager  *utils.PathManager
	proxyService *ProxyService
	log          *utils.Logger
	mu           sync.Mutex
	running      bool
}

// NewGCService creates a new garbage collection service
func NewGCService(cfg *config.Config, pathManager *utils.PathManager, proxyService *ProxyService, log *utils.Logger) *GCService {
	return &GCService{
		config:       cfg,
		pathManager:  pathManager,
		proxyService: proxyService,
		log:          log,
	}
}

// Run executes a full garbage collection cycle
// - Deletes orphan blobs (not referenced by any manifest)
// - Deletes proxy images not accessed in the last 30 days
func (gc *GCService) Run(dryRun bool) (*GCResult, error) {
	gc.mu.Lock()
	if gc.running {
		gc.mu.Unlock()
		return nil, nil // Already running
	}
	gc.running = true
	gc.mu.Unlock()

	defer func() {
		gc.mu.Lock()
		gc.running = false
		gc.mu.Unlock()
	}()

	start := time.Now()
	result := &GCResult{}

	gc.log.WithField("dryRun", dryRun).Info("Starting garbage collection")

	// Phase 1: Clean stale proxy images (not accessed in 30 days)
	staleResult, err := gc.cleanStaleProxyImages(dryRun)
	if err != nil {
		result.Errors = append(result.Errors, "stale images: "+err.Error())
	} else {
		result.StaleImagesDeleted = staleResult.deleted
		result.StaleImagesBytes = staleResult.bytes
	}

	// Phase 2: Clean orphan blobs (after stale images are deleted)
	orphanResult, err := gc.cleanOrphanBlobs(dryRun)
	if err != nil {
		result.Errors = append(result.Errors, "orphan blobs: "+err.Error())
	} else {
		result.OrphanBlobsDeleted = orphanResult.deleted
		result.OrphanBlobsBytes = orphanResult.bytes
	}

	result.TotalBytesReclaimed = result.OrphanBlobsBytes + result.StaleImagesBytes
	result.DurationMs = time.Since(start).Milliseconds()

	gc.log.WithFields(logrus.Fields{
		"orphanBlobs":   result.OrphanBlobsDeleted,
		"staleImages":   result.StaleImagesDeleted,
		"bytesReclaimed": result.TotalBytesReclaimed,
		"durationMs":    result.DurationMs,
		"dryRun":        dryRun,
	}).Info("Garbage collection completed")

	return result, nil
}

type cleanResult struct {
	deleted int
	bytes   int64
}

// cleanStaleProxyImages removes proxy images not accessed in the last 30 days
func (gc *GCService) cleanStaleProxyImages(dryRun bool) (*cleanResult, error) {
	result := &cleanResult{}
	cutoff := time.Now().AddDate(0, -1, 0) // 30 days ago

	gc.log.WithField("cutoff", cutoff).Debug("Scanning for stale proxy images")

	images, err := gc.proxyService.GetCachedImages()
	if err != nil {
		return result, err
	}

	for _, img := range images {
		// Only process proxy images
		if !strings.HasPrefix(img.Name, "proxy/") {
			continue
		}

		// Check if last accessed is before cutoff
		if img.LastAccessed.Before(cutoff) {
			gc.log.WithFields(logrus.Fields{
				"image":        img.Name,
				"tag":          img.Tag,
				"lastAccessed": img.LastAccessed,
				"size":         img.Size,
				"dryRun":       dryRun,
			}).Info("Found stale proxy image")

			result.deleted++
			result.bytes += img.Size

			if !dryRun {
				if err := gc.proxyService.DeleteCachedImage(img.Name, img.Tag); err != nil {
					gc.log.WithError(err).WithField("image", img.Name+":"+img.Tag).Warn("Failed to delete stale image")
				}
			}
		}
	}

	return result, nil
}

// cleanOrphanBlobs removes blobs not referenced by any manifest
func (gc *GCService) cleanOrphanBlobs(dryRun bool) (*cleanResult, error) {
	result := &cleanResult{}

	// Step 1: Collect all referenced digests from manifests
	referencedDigests := make(map[string]bool)

	basePath := gc.pathManager.GetBasePath()

	// Scan image manifests
	imagesDir := filepath.Join(basePath, "images")
	if err := gc.collectReferencedDigests(imagesDir, referencedDigests); err != nil {
		gc.log.WithError(err).Warn("Failed to scan images directory")
	}

	// Scan chart manifests
	manifestsDir := filepath.Join(basePath, "manifests")
	if err := gc.collectReferencedDigests(manifestsDir, referencedDigests); err != nil {
		gc.log.WithError(err).Warn("Failed to scan manifests directory")
	}

	gc.log.WithField("referencedCount", len(referencedDigests)).Debug("Collected referenced digests")

	// Step 2: Scan blobs directory and find orphans
	blobsDir := filepath.Join(basePath, "blobs")
	entries, err := os.ReadDir(blobsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		blobName := entry.Name()
		// Blob files are named by their digest (sha256:xxx or just the hash)
		digest := blobName
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}

		if !referencedDigests[digest] && !referencedDigests[blobName] {
			blobPath := filepath.Join(blobsDir, blobName)
			info, err := entry.Info()
			if err != nil {
				continue
			}

			gc.log.WithFields(logrus.Fields{
				"blob":   blobName,
				"size":   info.Size(),
				"dryRun": dryRun,
			}).Info("Found orphan blob")

			result.deleted++
			result.bytes += info.Size()

			if !dryRun {
				if err := os.Remove(blobPath); err != nil {
					gc.log.WithError(err).WithField("blob", blobName).Warn("Failed to delete orphan blob")
				}
			}
		}
	}

	return result, nil
}

// collectReferencedDigests walks a directory tree and extracts digests from manifests
func (gc *GCService) collectReferencedDigests(dir string, digests map[string]bool) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Try to parse as OCI/Docker manifest
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
			Layers []struct {
				Digest string `json:"digest"`
			} `json:"layers"`
			Manifests []struct {
				Digest string `json:"digest"`
			} `json:"manifests"`
		}

		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil
		}

		// Add config digest
		if manifest.Config.Digest != "" {
			digests[manifest.Config.Digest] = true
			// Also add without sha256: prefix for matching
			digests[strings.TrimPrefix(manifest.Config.Digest, "sha256:")] = true
		}

		// Add layer digests
		for _, layer := range manifest.Layers {
			if layer.Digest != "" {
				digests[layer.Digest] = true
				digests[strings.TrimPrefix(layer.Digest, "sha256:")] = true
			}
		}

		// Add manifest list digests (for multi-arch images)
		for _, m := range manifest.Manifests {
			if m.Digest != "" {
				digests[m.Digest] = true
				digests[strings.TrimPrefix(m.Digest, "sha256:")] = true
			}
		}

		return nil
	})
}

// GetStats returns current storage statistics
func (gc *GCService) GetStats() (*models.StorageStats, error) {
	basePath := gc.pathManager.GetBasePath()
	stats := &models.StorageStats{}

	// Count blobs
	blobsDir := filepath.Join(basePath, "blobs")
	if entries, err := os.ReadDir(blobsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				stats.BlobCount++
				if info, err := e.Info(); err == nil {
					stats.BlobsSize += info.Size()
				}
			}
		}
	}

	// Count cached images
	if images, err := gc.proxyService.GetCachedImages(); err == nil {
		stats.CachedImageCount = len(images)
		for _, img := range images {
			stats.CachedImagesSize += img.Size
		}
	}

	// Count charts
	chartsDir := filepath.Join(basePath, "charts")
	if entries, err := os.ReadDir(chartsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".tgz") {
				stats.ChartCount++
				if info, err := e.Info(); err == nil {
					stats.ChartsSize += info.Size()
				}
			}
		}
	}

	stats.TotalSize = stats.BlobsSize + stats.CachedImagesSize + stats.ChartsSize

	return stats, nil
}
