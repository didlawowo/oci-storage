// pkg/services/gc.go
package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/storage"
	"oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// GCResult contains the results of a garbage collection run
type GCResult struct {
	OrphanBlobsDeleted  int      `json:"orphanBlobsDeleted"`
	OrphanBlobsBytes    int64    `json:"orphanBlobsBytes"`
	StaleImagesDeleted  int      `json:"staleImagesDeleted"`
	StaleImagesBytes    int64    `json:"staleImagesBytes"`
	TotalBytesReclaimed int64    `json:"totalBytesReclaimed"`
	DurationMs          int64    `json:"durationMs"`
	Errors              []string `json:"errors,omitempty"`
}

// GCService handles garbage collection for orphan blobs and stale images
type GCService struct {
	config       *config.Config
	pathManager  *utils.PathManager
	backend      storage.Backend
	proxyService *ProxyService
	log          *utils.Logger
	mu           sync.Mutex
	running      bool
}

// NewGCService creates a new garbage collection service
func NewGCService(cfg *config.Config, pathManager *utils.PathManager, backend storage.Backend, proxyService *ProxyService, log *utils.Logger) *GCService {
	return &GCService{
		config:       cfg,
		pathManager:  pathManager,
		backend:      backend,
		proxyService: proxyService,
		log:          log,
	}
}

// Run executes a full garbage collection cycle
func (gc *GCService) Run(dryRun bool) (*GCResult, error) {
	gc.mu.Lock()
	if gc.running {
		gc.mu.Unlock()
		return nil, nil
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

	// Phase 1: Clean stale proxy images
	staleResult, err := gc.cleanStaleProxyImages(dryRun)
	if err != nil {
		result.Errors = append(result.Errors, "stale images: "+err.Error())
	} else {
		result.StaleImagesDeleted = staleResult.deleted
		result.StaleImagesBytes = staleResult.bytes
	}

	// Phase 2: Clean orphan blobs
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
		"orphanBlobs":    result.OrphanBlobsDeleted,
		"staleImages":    result.StaleImagesDeleted,
		"bytesReclaimed": result.TotalBytesReclaimed,
		"durationMs":     result.DurationMs,
		"dryRun":         dryRun,
	}).Info("Garbage collection completed")

	return result, nil
}

type cleanResult struct {
	deleted int
	bytes   int64
}

func (gc *GCService) cleanStaleProxyImages(dryRun bool) (*cleanResult, error) {
	result := &cleanResult{}
	cutoff := time.Now().AddDate(0, -1, 0) // 30 days ago

	gc.log.WithField("cutoff", cutoff).Debug("Scanning for stale proxy images")

	images, err := gc.proxyService.GetCachedImages()
	if err != nil {
		return result, err
	}

	for _, img := range images {
		if !strings.HasPrefix(img.Name, "proxy/") {
			continue
		}

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

func (gc *GCService) cleanOrphanBlobs(dryRun bool) (*cleanResult, error) {
	result := &cleanResult{}

	// Collect all referenced digests from manifests
	referencedDigests := make(map[string]bool)

	// Scan image manifests
	gc.collectReferencedDigests("images", referencedDigests)

	// Scan chart manifests
	gc.collectReferencedDigests("manifests", referencedDigests)

	gc.log.WithField("referencedCount", len(referencedDigests)).Debug("Collected referenced digests")

	// Scan blobs directory and find orphans
	entries, err := gc.backend.List("blobs")
	if err != nil {
		return result, nil
	}

	for _, entry := range entries {
		if entry.IsDir {
			continue
		}

		blobName := entry.Name
		digest := blobName
		if !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}

		if !referencedDigests[digest] && !referencedDigests[blobName] {
			gc.log.WithFields(logrus.Fields{
				"blob":   blobName,
				"size":   entry.Size,
				"dryRun": dryRun,
			}).Info("Found orphan blob")

			result.deleted++
			result.bytes += entry.Size

			if !dryRun {
				blobPath := filepath.Join("blobs", blobName)
				if err := gc.backend.Delete(blobPath); err != nil {
					gc.log.WithError(err).WithField("blob", blobName).Warn("Failed to delete orphan blob")
				}
			}
		}
	}

	return result, nil
}

// collectReferencedDigests walks a directory tree and extracts digests from manifests
func (gc *GCService) collectReferencedDigests(dir string, digests map[string]bool) {
	gc.walkAndCollectDigests(dir, digests)
}

// walkAndCollectDigests recursively walks directories via Backend.List and extracts digests from JSON manifests
func (gc *GCService) walkAndCollectDigests(dir string, digests map[string]bool) {
	entries, err := gc.backend.List(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		fullPath := filepath.Join(dir, entry.Name)

		if entry.IsDir {
			gc.walkAndCollectDigests(fullPath, digests)
			continue
		}

		if !strings.HasSuffix(entry.Name, ".json") {
			continue
		}

		data, err := gc.backend.Read(fullPath)
		if err != nil {
			continue
		}

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
			continue
		}

		if manifest.Config.Digest != "" {
			digests[manifest.Config.Digest] = true
			digests[strings.TrimPrefix(manifest.Config.Digest, "sha256:")] = true
		}

		for _, layer := range manifest.Layers {
			if layer.Digest != "" {
				digests[layer.Digest] = true
				digests[strings.TrimPrefix(layer.Digest, "sha256:")] = true
			}
		}

		for _, m := range manifest.Manifests {
			if m.Digest != "" {
				digests[m.Digest] = true
				digests[strings.TrimPrefix(m.Digest, "sha256:")] = true
			}
		}
	}
}

// GetStats returns current storage statistics
func (gc *GCService) GetStats() (*models.StorageStats, error) {
	stats := &models.StorageStats{}

	// Count blobs
	if entries, err := gc.backend.List("blobs"); err == nil {
		for _, e := range entries {
			if !e.IsDir {
				stats.BlobCount++
				stats.BlobsSize += e.Size
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
	if entries, err := gc.backend.List("charts"); err == nil {
		for _, e := range entries {
			if !e.IsDir && strings.HasSuffix(e.Name, ".tgz") {
				stats.ChartCount++
				stats.ChartsSize += e.Size
			}
		}
	}

	stats.TotalSize = stats.BlobsSize + stats.CachedImagesSize + stats.ChartsSize

	return stats, nil
}
