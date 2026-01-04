package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/pkg/models"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

// proxyBlob fetches a blob from upstream, caches it completely, then serves from cache
func (h *OCIHandler) proxyBlob(c *fiber.Ctx, name, digest string) error {
	// Acquire semaphore to limit concurrent downloads (prevents OOM with large images)
	select {
	case blobDownloadSemaphore <- struct{}{}:
		defer func() { <-blobDownloadSemaphore }()
	case <-c.Context().Done():
		return c.SendStatus(408) // Request timeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	registryURL, upstreamName, err := h.proxyService.ResolveRegistry(name)
	if err != nil {
		h.log.WithError(err).Error("Failed to resolve registry for blob")
		return c.SendStatus(404)
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"registry":     registryURL,
		"upstreamName": upstreamName,
		"digest":       digest,
	}).Debug("Fetching blob from upstream (semaphore acquired)")

	reader, size, err := h.proxyService.GetBlob(ctx, registryURL, upstreamName, digest)
	if err != nil {
		h.log.WithError(err).Error("Failed to fetch blob from upstream")
		return c.SendStatus(502)
	}
	defer reader.Close()

	blobPath := h.pathManager.GetBlobPath(digest)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		h.log.WithError(err).Warn("Failed to create blob directory")
	}

	// Download completely to cache first, then serve from cache
	// This prevents race conditions with concurrent requests
	file, err := os.Create(blobPath)
	if err != nil {
		h.log.WithError(err).Warn("Failed to create blob cache file, streaming without caching")
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		if size > 0 {
			c.Set("Content-Length", fmt.Sprintf("%d", size))
		}
		return c.SendStream(reader)
	}

	written, err := io.Copy(file, reader)
	file.Close()
	if err != nil {
		h.log.WithError(err).Error("Failed to download blob to cache")
		os.Remove(blobPath)
		return c.SendStatus(502)
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"digest": digest,
		"size":   written,
	}).Info("Blob proxied and cached successfully")

	// Serve from cache
	c.Set("Docker-Content-Digest", digest)
	c.Set("Content-Type", "application/octet-stream")
	return c.SendFile(blobPath)
}

// proxyManifest fetches a manifest from upstream and caches it
func (h *OCIHandler) proxyManifest(c *fiber.Ctx, name, reference string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	registryURL, upstreamName, err := h.proxyService.ResolveRegistry(name)
	if err != nil {
		h.log.WithError(err).Error("Failed to resolve registry")
		return c.SendStatus(404)
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"registry":     registryURL,
		"upstreamName": upstreamName,
		"reference":    reference,
	}).Debug("Fetching manifest from upstream")

	manifestData, contentType, err := h.proxyService.GetManifest(ctx, registryURL, upstreamName, reference)
	if err != nil {
		h.log.WithError(err).Error("Failed to fetch manifest from upstream")
		return c.SendStatus(502)
	}

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	// Cache manifest as blob (for digest-based lookups of child manifests)
	go func() {
		blobPath := h.pathManager.GetBlobPath(digest)
		if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err == nil {
			if err := os.WriteFile(blobPath, manifestData, 0644); err != nil {
				h.log.WithError(err).Warn("Failed to cache manifest as blob")
			}
		}
	}()

	// Cache locally - only for tag references
	if !strings.HasPrefix(reference, "sha256:") {
		go h.cacheManifest(name, reference, manifestData, registryURL, upstreamName)
	}

	c.Set("Content-Type", contentType)
	c.Set("Docker-Content-Digest", digest)

	if c.Method() == "HEAD" {
		c.Set("Content-Length", fmt.Sprintf("%d", len(manifestData)))
		return c.Status(200).Send(nil)
	}

	return c.Send(manifestData)
}

// cacheManifest saves a proxied manifest to local storage
func (h *OCIHandler) cacheManifest(name, reference string, manifestData []byte, registryURL, upstreamName string) {
	var manifest models.OCIManifest
	var totalSize int64

	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		h.log.WithError(err).Warn("Failed to parse manifest for caching")
		return
	}

	isManifestList := manifest.MediaType == models.MediaTypeOCIManifestList ||
		manifest.MediaType == models.MediaTypeDockerManifestList

	if isManifestList {
		var index models.OCIIndex
		if err := json.Unmarshal(manifestData, &index); err != nil {
			h.log.WithError(err).Warn("Failed to parse manifest list")
			return
		}

		// Calculate total size by fetching the first platform manifest and summing its layers
		// This gives the actual image size, not just manifest sizes
		totalSize = h.calculateManifestListSize(index, registryURL, upstreamName)

		h.log.WithFields(logrus.Fields{
			"name":      name,
			"reference": reference,
			"type":      "manifest_list",
			"children":  len(index.Manifests),
			"totalSize": totalSize,
		}).Debug("Caching manifest list")

		// For manifest lists, save the raw bytes directly to preserve the manifests array
		// Don't use SaveImage as it will corrupt the data by re-marshaling as OCIManifest
		manifestPath := h.pathManager.GetImageManifestPath(name, reference)
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err == nil {
			if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
				h.log.WithError(err).Warn("Failed to save manifest list")
			}
		}

		// Save metadata so the image appears in /images listing
		if h.imageService != nil {
			if err := h.imageService.SaveImageIndex(name, reference, manifestData, totalSize); err != nil {
				h.log.WithError(err).Warn("Failed to save manifest list metadata")
			}
		}

		go h.prefetchPlatformManifests(index, registryURL, upstreamName)
	} else {
		totalSize = manifest.GetTotalSize()
		if h.imageService != nil {
			if err := h.imageService.SaveImage(name, reference, &manifest); err != nil {
				h.log.WithError(err).Warn("Failed to cache manifest via image service")
			}
		}
	}

	registryName := "docker.io"
	for _, reg := range h.config.Proxy.Registries {
		if reg.URL == registryURL {
			registryName = reg.Name
			break
		}
	}

	cacheMetadata := models.CachedImageMetadata{
		Name:           name,
		Tag:            reference,
		Digest:         fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData)),
		SourceRegistry: registryName,
		OriginalRef:    upstreamName + ":" + reference,
		Size:           totalSize,
		CachedAt:       time.Now(),
		LastAccessed:   time.Now(),
		AccessCount:    1,
	}

	if err := h.proxyService.AddToCache(cacheMetadata); err != nil {
		h.log.WithError(err).Warn("Failed to add to cache tracking")
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
		"registry":  registryName,
	}).Info("Manifest cached successfully")
}

// prefetchPlatformManifests pre-fetches and caches manifests for common platforms (amd64, arm64)
func (h *OCIHandler) prefetchPlatformManifests(index models.OCIIndex, registryURL, upstreamName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	targetPlatforms := map[string]bool{
		"linux/amd64": true,
		"linux/arm64": true,
	}

	for _, desc := range index.Manifests {
		if desc.Platform == nil {
			continue
		}

		platformKey := desc.Platform.OS + "/" + desc.Platform.Architecture
		if !targetPlatforms[platformKey] {
			continue
		}

		h.log.WithFields(logrus.Fields{
			"platform": platformKey,
			"digest":   desc.Digest,
		}).Debug("Prefetching platform manifest")

		manifestData, _, err := h.proxyService.GetManifest(ctx, registryURL, upstreamName, desc.Digest)
		if err != nil {
			h.log.WithError(err).WithField("platform", platformKey).Warn("Failed to prefetch platform manifest")
			continue
		}

		blobPath := h.pathManager.GetBlobPath(desc.Digest)
		if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err == nil {
			if err := os.WriteFile(blobPath, manifestData, 0644); err != nil {
				h.log.WithError(err).Warn("Failed to cache platform manifest as blob")
			} else {
				h.log.WithFields(logrus.Fields{
					"platform": platformKey,
					"digest":   desc.Digest,
					"size":     len(manifestData),
				}).Info("Platform manifest prefetched and cached")
			}
		}
	}
}

// calculateManifestListSize calculates the total size of an image from a manifest list
// by fetching the first platform manifest (preferably linux/amd64) and summing its layers
func (h *OCIHandler) calculateManifestListSize(index models.OCIIndex, registryURL, upstreamName string) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Prefer linux/amd64, fall back to first available
	var targetDigest string
	for _, desc := range index.Manifests {
		if desc.Platform != nil && desc.Platform.OS == "linux" && desc.Platform.Architecture == "amd64" {
			targetDigest = desc.Digest
			break
		}
	}
	if targetDigest == "" && len(index.Manifests) > 0 {
		targetDigest = index.Manifests[0].Digest
	}
	if targetDigest == "" {
		return 0
	}

	// Fetch the platform manifest
	manifestData, _, err := h.proxyService.GetManifest(ctx, registryURL, upstreamName, targetDigest)
	if err != nil {
		h.log.WithError(err).Debug("Failed to fetch platform manifest for size calculation")
		return 0
	}

	var platformManifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &platformManifest); err != nil {
		h.log.WithError(err).Debug("Failed to parse platform manifest for size calculation")
		return 0
	}

	return platformManifest.GetTotalSize()
}
