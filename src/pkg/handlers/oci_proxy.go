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

	"net/http"
)

// calculateBlobTimeout returns a dynamic timeout based on blob size
// Formula: base + (size_in_gb * per_gb_seconds), capped at max
func (h *OCIHandler) calculateBlobTimeout(sizeBytes int64) time.Duration {
	cfg := h.config.Proxy.Timeout

	// Base timeout
	timeout := time.Duration(cfg.BlobBaseSeconds) * time.Second

	// Add time per GB if size is known
	if sizeBytes > 0 {
		sizeGB := float64(sizeBytes) / (1024 * 1024 * 1024)
		additionalSeconds := int(sizeGB * float64(cfg.BlobPerGBSeconds))
		timeout += time.Duration(additionalSeconds) * time.Second
	}

	// Cap at maximum
	maxTimeout := time.Duration(cfg.MaxTimeoutMinutes) * time.Minute
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	return timeout
}

// proxyBlob fetches a blob from upstream, caches it completely, then serves from cache
func (h *OCIHandler) proxyBlob(c *fiber.Ctx, name, digest string) error {
	// Check if blob is already cached before doing anything
	blobPath := h.pathManager.GetBlobPath(digest)
	if exists, _ := h.backend.Exists(blobPath); exists {
		h.log.WithField("digest", digest).Debug("Blob already cached, serving from cache")
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return h.sendBlob(c, blobPath)
	}

	registryURL, upstreamName, err := h.proxyService.ResolveRegistry(name)
	if err != nil {
		h.log.WithError(err).Error("Failed to resolve registry for blob")
		return c.SendStatus(404)
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"registry":     registryURL,
		"upstreamName": upstreamName,
		"digest":       digest,
	}).Debug("Fetching blob from upstream")

	// Calculate dynamic timeout based on estimated blob size (use max timeout for initial fetch)
	// We use a generous initial timeout to establish connection and get the size header
	initialTimeout := time.Duration(h.config.Proxy.Timeout.MaxTimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), initialTimeout)
	defer cancel()

	reader, size, err := h.proxyService.GetBlob(ctx, registryURL, upstreamName, digest)
	if err != nil {
		h.log.WithError(err).Error("Failed to fetch blob from upstream")
		return c.SendStatus(502)
	}
	defer reader.Close()

	// Log the actual size-based timeout that would be calculated
	timeout := h.calculateBlobTimeout(size)

	h.log.WithFields(logrus.Fields{
		"digest":  digest,
		"size":    size,
		"timeout": timeout.String(),
	}).Debug("Calculated dynamic timeout for blob download")

	// Select semaphore based on blob size: small (<100MB) or large (>=100MB)
	var sem chan struct{}
	var sizeCategory string
	if size > 0 && size >= blobSizeThreshold {
		sem = largeBlobSemaphore
		sizeCategory = "large"
	} else {
		sem = smallBlobSemaphore
		sizeCategory = "small"
	}

	// Acquire size-appropriate semaphore with timeout
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		h.log.WithField("digest", digest).Warn("Timeout waiting for semaphore")
		return c.SendStatus(504) // Gateway timeout
	case <-c.Context().Done():
		return c.SendStatus(408) // Request timeout
	}

	h.log.WithFields(logrus.Fields{
		"digest":   digest,
		"size":     size,
		"category": sizeCategory,
	}).Debug("Semaphore acquired for blob download")

	// Double-check after acquiring semaphore (another goroutine may have completed download)
	if exists, _ := h.backend.Exists(blobPath); exists {
		h.log.WithField("digest", digest).Debug("Blob cached by another request, serving from cache")
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return h.sendBlob(c, blobPath)
	}

	// Download to a local temp file, then import to backend
	tempDir := filepath.Dir(h.pathManager.GetTempPath("proxy"))
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		h.log.WithError(err).Warn("Failed to create temp directory")
	}

	tmpFile, err := os.CreateTemp(tempDir, "proxy-blob-*")
	if err != nil {
		h.log.WithError(err).Warn("Failed to create blob cache file, streaming without caching")
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		if size > 0 {
			c.Set("Content-Length", fmt.Sprintf("%d", size))
			return c.SendStream(reader, int(size))
		}
		return c.SendStream(reader)
	}
	tempPath := tmpFile.Name()

	written, err := io.Copy(tmpFile, reader)
	tmpFile.Close()
	if err != nil {
		h.log.WithError(err).Error("Failed to download blob to cache")
		os.Remove(tempPath)
		return c.SendStatus(502)
	}

	// Verify size matches expected (if known) to detect truncated downloads
	if size > 0 && written != size {
		h.log.WithFields(logrus.Fields{
			"digest":   digest,
			"expected": size,
			"written":  written,
		}).Error("Blob size mismatch - download truncated")
		os.Remove(tempPath)
		return c.SendStatus(502)
	}

	// Import temp file to backend storage
	if err := h.backend.Import(tempPath, blobPath); err != nil {
		h.log.WithError(err).Error("Failed to import blob to storage")
		os.Remove(tempPath)
		return c.SendStatus(502)
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"digest": digest,
		"size":   written,
	}).Info("Blob proxied and cached successfully")

	// Serve from backend
	c.Set("Docker-Content-Digest", digest)
	c.Set("Content-Type", "application/octet-stream")
	return h.sendBlob(c, blobPath)
}

// proxyManifest fetches a manifest from upstream and caches it
func (h *OCIHandler) proxyManifest(c *fiber.Ctx, name, reference string) error {
	manifestTimeout := time.Duration(h.config.Proxy.Timeout.ManifestSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), manifestTimeout)
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
		if err := h.backend.Write(blobPath, manifestData); err != nil {
			h.log.WithError(err).Warn("Failed to cache manifest as blob")
		}
	}()

	// Cache locally - only for tag references
	if !strings.HasPrefix(reference, "sha256:") {
		go h.cacheManifest(name, reference, manifestData, registryURL, upstreamName)
	}

	// Trigger async vulnerability scan for proxied images (skip Helm charts)
	if h.scanService != nil && h.scanService.IsEnabled() {
		var manifest models.OCIManifest
		if err := json.Unmarshal(manifestData, &manifest); err == nil {
			if models.DetectArtifactType(&manifest) != models.ArtifactTypeHelmChart {
				h.scanService.ScanImage(name, reference, digest)
			} else {
				h.log.WithFunc().WithField("name", name).Debug("Skipping scan for Helm chart artifact")
			}
		}
	}

	// Security gate check before serving proxied manifest
	if blocked, resp := h.checkScanGate(c, manifestData, name); blocked {
		return resp
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
	// Normalize name to avoid duplicates (traefik vs library/traefik)
	name = normalizeDockerHubName(name)
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
		if err := h.backend.Write(manifestPath, manifestData); err != nil {
			h.log.WithError(err).Warn("Failed to save manifest list")
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
		if err := h.backend.Write(blobPath, manifestData); err != nil {
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

// proxyHeadBlob checks if a blob exists on the upstream registry without downloading it.
// This enables container runtimes to do HEAD checks for proxy images before pulling.
func (h *OCIHandler) proxyHeadBlob(c *fiber.Ctx, name, digest string) error {
	registryURL, upstreamName, err := h.proxyService.ResolveRegistry(name)
	if err != nil {
		h.log.WithError(err).Error("Failed to resolve registry for HEAD blob")
		return c.SendStatus(404)
	}

	manifestTimeout := time.Duration(h.config.Proxy.Timeout.ManifestSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), manifestTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, upstreamName, digest)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		h.log.WithError(err).Error("Failed to create HEAD request")
		return c.SendStatus(502)
	}

	resp, err := h.proxyService.FetchWithAuth(ctx, req, registryURL, upstreamName)
	if err != nil {
		h.log.WithError(err).Error("Failed to HEAD blob from upstream")
		return c.SendStatus(502)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			c.Set("Content-Length", cl)
		}
		return c.SendStatus(200)
	}

	return c.SendStatus(resp.StatusCode)
}
