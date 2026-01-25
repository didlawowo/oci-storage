package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oci-storage/config"
	interfaces "oci-storage/pkg/interfaces"
	"oci-storage/pkg/models"
	utils "oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// Blob download semaphores - split by size for better throughput
// Small blobs (<100MB): 5 concurrent - quick layers, configs, manifests
// Large blobs (>=100MB): 7 concurrent - big model layers, base images
// Total: 12 parallel downloads
const blobSizeThreshold = 100 * 1024 * 1024 // 100MB

var (
	smallBlobSemaphore = make(chan struct{}, 5)
	largeBlobSemaphore = make(chan struct{}, 7)
)

type OCIHandler struct {
	log          *utils.Logger
	chartService interfaces.ChartServiceInterface
	imageService interfaces.ImageServiceInterface
	proxyService interfaces.ProxyServiceInterface
	pathManager  *utils.PathManager
	config       *config.Config
}

func NewOCIHandler(
	chartService interfaces.ChartServiceInterface,
	imageService interfaces.ImageServiceInterface,
	proxyService interfaces.ProxyServiceInterface,
	cfg *config.Config,
	log *utils.Logger,
) *OCIHandler {
	return &OCIHandler{
		chartService: chartService,
		imageService: imageService,
		proxyService: proxyService,
		config:       cfg,
		log:          log,
		pathManager:  chartService.GetPathManager(),
	}
}

func (h *OCIHandler) HandleOCIAPI(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Processing API request")
	return c.JSON(fiber.Map{
		"apiVersion":            "2.0",
		"docker-content-digest": true,
		"oci-distribution-spec": "v1.0",
	})
}

func (h *OCIHandler) GetBlob(c *fiber.Ctx) error {
	digest := c.Params("digest")
	name := h.getName(c)

	// Validate inputs to prevent path traversal
	if err := utils.ValidateDigest(digest); err != nil {
		h.log.WithField("digest", digest).Warn("Invalid digest format")
		return HTTPError(c, 400, "Invalid digest format")
	}
	if err := utils.ValidateRepoName(name); err != nil {
		h.log.WithField("name", name).Warn("Invalid repository name")
		return HTTPError(c, 400, "Invalid repository name")
	}

	// Normalize Docker Hub names for consistent cache lookup
	normalizedName := h.normalizeImageName(name)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":           name,
		"normalizedName": normalizedName,
		"digest":         digest,
	}).Debug("Processing blob download request")

	// Try local first - use SendFile to avoid loading entire blob into memory
	blobPath := h.pathManager.GetBlobPath(digest)
	if _, err := os.Stat(blobPath); err == nil {
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return c.SendFile(blobPath)
	}

	// Not found locally - try proxy if enabled
	if h.proxyService != nil && h.proxyService.IsEnabled() {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":           normalizedName,
			"digest":         digest,
		}).Debug("Blob not found locally, trying proxy")

		return h.proxyBlob(c, normalizedName, digest)
	}

	h.log.WithFunc().Debug("Blob not found")
	return c.SendStatus(404)
}

func (h *OCIHandler) HandleCatalog(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Processing catalog request")

	repositories := make([]string, 0)

	charts, err := h.chartService.ListCharts()
	if err != nil {
		h.log.WithFunc().WithError(err).Warn("Failed to list charts")
	} else {
		for _, chart := range charts {
			repositories = append(repositories, chart.Name)
		}
	}

	if h.imageService != nil {
		images, err := h.imageService.ListImages()
		if err != nil {
			h.log.WithFunc().WithError(err).Warn("Failed to list images")
		} else {
			for _, image := range images {
				found := false
				for _, r := range repositories {
					if r == image.Name {
						found = true
						break
					}
				}
				if !found {
					repositories = append(repositories, image.Name)
				}
			}
		}
	}

	return c.JSON(fiber.Map{
		"repositories": repositories,
	})
}

// HandleListTags returns all tags for a repository (OCI Distribution Spec)
func (h *OCIHandler) HandleListTags(c *fiber.Ctx) error {
	name := h.getName(c)

	h.log.WithFunc().WithField("name", name).Debug("Processing tags list request")

	tags := make([]string, 0)

	if h.imageService != nil {
		imageTags, err := h.imageService.ListTags(name)
		if err == nil && len(imageTags) > 0 {
			tags = append(tags, imageTags...)
		}
	}

	charts, err := h.chartService.ListCharts()
	if err == nil {
		for _, chart := range charts {
			if chart.Name == name {
				for _, version := range chart.Versions {
					tags = append(tags, version.Version)
				}
				break
			}
		}
	}

	return c.JSON(fiber.Map{
		"name": name,
		"tags": tags,
	})
}

func (h *OCIHandler) HandleManifest(c *fiber.Ctx) error {
	name := h.getName(c)
	reference := c.Params("reference")

	// Validate inputs to prevent path traversal
	if err := utils.ValidateRepoName(name); err != nil {
		h.log.WithField("name", name).Warn("Invalid repository name")
		return HTTPError(c, 400, "Invalid repository name")
	}
	if err := utils.ValidateReference(reference); err != nil {
		h.log.WithField("reference", reference).Warn("Invalid reference format")
		return HTTPError(c, 400, "Invalid reference format")
	}

	// Normalize Docker Hub names for cache lookup (traefik -> library/traefik)
	normalizedName := h.normalizeImageName(name)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":           name,
		"normalizedName": normalizedName,
		"reference":      reference,
	}).Debug("Processing manifest request")

	// Try to find manifest in local storage first (use normalized name for cache consistency)
	manifestData, manifestPath, err := h.findManifest(normalizedName, reference)
	if err == nil {
		h.log.WithFunc().WithFields(logrus.Fields{
			"manifestPath": manifestPath,
			"source":       "local",
		}).Debug("Found manifest locally")

		if h.proxyService != nil && h.proxyService.IsEnabled() {
			h.proxyService.UpdateAccessTime(normalizedName, reference)
		}

		return h.sendManifestResponse(c, manifestData, reference)
	}

	// Not found locally - try proxy if enabled
	// ONLY proxy for paths starting with "proxy/" - this protects charts/ and images/ from being proxied
	// HEAD requests are allowed for proxy paths (needed for container runtime manifest checks)
	isProxyPath := strings.HasPrefix(normalizedName, "proxy/")
	shouldProxy := h.proxyService != nil && h.proxyService.IsEnabled() && isProxyPath

	h.log.WithFunc().WithFields(logrus.Fields{
		"shouldProxy":     shouldProxy,
		"isProxyPath":     isProxyPath,
		"method":          c.Method(),
	}).Debug("Proxy decision")

	if shouldProxy {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":           normalizedName,
			"reference":      reference,
		}).Debug("Manifest not found locally, trying proxy")

		return h.proxyManifest(c, normalizedName, reference)
	}

	h.log.WithFunc().WithError(err).Debug("Manifest not found")
	if c.Method() == "HEAD" {
		return c.Status(404).Send(nil)
	}
	return c.SendStatus(404)
}

// findManifest searches for a manifest in all possible locations
func (h *OCIHandler) findManifest(name, reference string) ([]byte, string, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return h.findManifestByDigest(name, reference)
	}

	searchPaths := []string{
		filepath.Join(h.pathManager.GetBasePath(), "manifests", name, reference+".json"),
		h.pathManager.GetImageManifestPath(name, reference),
	}

	for _, path := range searchPaths {
		if data, err := os.ReadFile(path); err == nil {
			return data, path, nil
		}
	}

	return nil, "", fmt.Errorf("manifest not found for %s:%s", name, reference)
}

// findManifestByDigest searches for a manifest by its digest
func (h *OCIHandler) findManifestByDigest(name, digest string) ([]byte, string, error) {
	// First try blob path (most reliable - stored with correct digest)
	blobPath := h.pathManager.GetBlobPath(digest)
	if data, err := os.ReadFile(blobPath); err == nil {
		return data, blobPath, nil
	}

	// Try finding by filename pattern (sha256_xxx.json) - faster than recalculating hashes
	// This handles cases where manifest was stored with digest in filename
	digestFileName := strings.Replace(digest, ":", "_", 1) + ".json"

	helmManifestsDir := filepath.Join(h.pathManager.GetBasePath(), "manifests", name)
	helmManifestPath := filepath.Join(helmManifestsDir, digestFileName)
	if data, err := os.ReadFile(helmManifestPath); err == nil {
		return data, helmManifestPath, nil
	}

	imageManifestsDir := filepath.Join(h.pathManager.GetBasePath(), "images", name, "manifests")
	imageManifestPath := filepath.Join(imageManifestsDir, digestFileName)
	if data, err := os.ReadFile(imageManifestPath); err == nil {
		return data, imageManifestPath, nil
	}

	// Fallback: search by recalculating hashes (slow but thorough)
	if data, path, err := h.searchDirForDigest(helmManifestsDir, digest); err == nil {
		return data, path, nil
	}

	if data, path, err := h.searchDirForDigest(imageManifestsDir, digest); err == nil {
		return data, path, nil
	}

	return nil, "", fmt.Errorf("manifest with digest %s not found", digest)
}

// searchDirForDigest searches a directory for a file matching the given digest
func (h *OCIHandler) searchDirForDigest(dir, targetDigest string) ([]byte, string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}
		filePath := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		if currentDigest == targetDigest {
			return data, filePath, nil
		}
	}

	return nil, "", fmt.Errorf("digest not found in %s", dir)
}

func (h *OCIHandler) getBlobByDigest(digest string) ([]byte, error) {
	blobPath := h.pathManager.GetBlobPath(digest)
	h.log.WithFunc().WithField("path", blobPath).Debug("Retrieving blob")

	chartData, err := os.ReadFile(blobPath)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to read blob data")
		return nil, fmt.Errorf("failed to read blob: %w", err)
	}

	return chartData, nil
}

func calculateDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", hash)
}

func generateUUID() string {
	return uuid.New().String()
}

func (h *OCIHandler) PutBlob(c *fiber.Ctx) error {
	digest := calculateDigest(c.Body())
	blobPath := h.pathManager.GetBlobPath(digest)

	h.log.WithFunc().WithFields(logrus.Fields{
		"digest": digest,
		"path":   blobPath,
	}).Debug("Processing blob upload")

	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create blob directory")
		return err
	}

	if err := os.WriteFile(blobPath, c.Body(), 0644); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to write blob")
		return err
	}

	c.Set("Docker-Content-Digest", digest)
	return c.SendStatus(201)
}

func (h *OCIHandler) PostUpload(c *fiber.Ctx) error {
	name := h.getName(c)
	uuid := generateUUID()

	h.log.WithFunc().WithFields(logrus.Fields{
		"name": name,
		"uuid": uuid,
	}).Debug("Initializing upload")

	// Build absolute URL for Location header (required by crane and other OCI clients)
	scheme := "http"
	if c.Protocol() == "https" {
		scheme = "https"
	}
	location := fmt.Sprintf("%s://%s/v2/%s/blobs/uploads/%s", scheme, c.Hostname(), name, uuid)
	c.Set("Location", location)
	c.Set("Docker-Upload-UUID", uuid)
	return c.SendStatus(202)
}

func (h *OCIHandler) PatchBlob(c *fiber.Ctx) error {
	name := h.getName(c)
	uuid := c.Params("uuid")

	// Validate UUID to prevent path traversal
	if err := utils.ValidateUUID(uuid); err != nil {
		h.log.WithField("uuid", uuid).Warn("Invalid UUID format")
		return HTTPError(c, 400, "Invalid UUID format")
	}

	tempPath := h.pathManager.GetTempPath(uuid)

	h.log.WithFunc().WithFields(logrus.Fields{
		"uuid": uuid,
		"size": len(c.Body()),
		"path": tempPath,
	}).Debug("Processing PATCH request")

	if err := os.MkdirAll(filepath.Dir(tempPath), 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create temp directory")
		return c.SendStatus(500)
	}

	if len(c.Body()) == 0 {
		h.log.WithFunc().Error("Received empty body")
		return HTTPError(c, 400, "Empty body")
	}

	if err := os.WriteFile(tempPath, c.Body(), 0644); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to write temp file")
		return c.SendStatus(500)
	}

	h.log.WithFunc().Info("Successfully processed PATCH data")

	// Build absolute URL for Location header (required by OCI clients like crane)
	scheme := "http"
	if c.Protocol() == "https" {
		scheme = "https"
	}
	location := fmt.Sprintf("%s://%s/v2/%s/blobs/uploads/%s", scheme, c.Hostname(), name, uuid)
	c.Set("Location", location)
	c.Set("Docker-Upload-UUID", uuid)
	c.Set("Range", fmt.Sprintf("0-%d", len(c.Body())-1))
	return c.SendStatus(202)
}

func (h *OCIHandler) CompleteUpload(c *fiber.Ctx) error {
	name := h.getName(c)
	uuid := c.Params("uuid")
	digest := c.Query("digest")

	// Validate all inputs to prevent path traversal
	if err := utils.ValidateRepoName(name); err != nil {
		h.log.WithField("name", name).Warn("Invalid repository name")
		return HTTPError(c, 400, "Invalid repository name")
	}
	if err := utils.ValidateUUID(uuid); err != nil {
		h.log.WithField("uuid", uuid).Warn("Invalid UUID format")
		return HTTPError(c, 400, "Invalid UUID format")
	}
	if err := utils.ValidateDigest(digest); err != nil {
		h.log.WithField("digest", digest).Warn("Invalid digest format")
		return HTTPError(c, 400, "Invalid digest format")
	}

	tempPath := h.pathManager.GetTempPath(uuid)
	finalPath := h.pathManager.GetBlobPath(digest)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"uuid":      uuid,
		"digest":    digest,
		"tempPath":  tempPath,
		"finalPath": finalPath,
	}).Debug("Completing upload")

	if len(c.Body()) > 0 {
		if err := os.WriteFile(tempPath, c.Body(), 0644); err != nil {
			h.log.WithFunc().WithError(err).Error("Failed to write final data")
			return c.SendStatus(500)
		}
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to finalize upload")
		return c.SendStatus(500)
	}

	c.Set("Docker-Content-Digest", digest)
	h.log.WithFunc().WithField("name", name).Info("Upload completed successfully")
	return c.SendStatus(201)
}

func (h *OCIHandler) HeadBlob(c *fiber.Ctx) error {
	digest := c.Params("digest")
	name := h.getName(c)

	// Validate inputs to prevent path traversal
	if err := utils.ValidateDigest(digest); err != nil {
		h.log.WithField("digest", digest).Warn("Invalid digest format")
		return HTTPError(c, 400, "Invalid digest format")
	}
	if err := utils.ValidateRepoName(name); err != nil {
		h.log.WithField("name", name).Warn("Invalid repository name")
		return HTTPError(c, 400, "Invalid repository name")
	}

	blobPath := h.pathManager.GetBlobPath(digest)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":   name,
		"digest": digest,
		"path":   blobPath,
	}).Debug("Processing HEAD request")

	info, err := os.Stat(blobPath)
	if err == nil {
		c.Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return c.SendStatus(200)
	}

	if os.IsNotExist(err) {
		h.log.WithFunc().Debug("Blob not found locally")
		return c.SendStatus(404)
	}

	h.log.WithFunc().WithError(err).Error("Failed to check blob")
	return c.SendStatus(500)
}

func (h *OCIHandler) PutManifest(c *fiber.Ctx) error {
	name := h.getName(c)
	reference := c.Params("reference")

	// Validate inputs to prevent path traversal
	if err := utils.ValidateRepoName(name); err != nil {
		h.log.WithField("name", name).Warn("Invalid repository name")
		return HTTPError(c, 400, "Invalid repository name")
	}
	if err := utils.ValidateReference(reference); err != nil {
		h.log.WithField("reference", reference).Warn("Invalid reference format")
		return HTTPError(c, 400, "Invalid reference format")
	}

	// Block push to /helm/ - use /charts/ instead
	if strings.HasPrefix(name, "helm/") {
		h.log.WithField("name", name).Warn("Push to /helm/ is not allowed, use /charts/ instead")
		return HTTPError(c, 400, "Push to /helm/ is not allowed. Use /charts/ for Helm charts.")
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
	}).Debug("Processing manifest upload")

	// CRITICAL: Use the raw body bytes for storage to preserve exact content
	// Re-marshaling JSON changes field order and formatting, corrupting the digest
	manifestData := c.Body()
	digest := sha256.Sum256(manifestData)
	digestStr := fmt.Sprintf("sha256:%x", digest)

	// Always save manifest to blob storage first (for digest-based lookups)
	// This ensures the exact bytes are preserved and can be retrieved by digest
	blobPath := h.pathManager.GetBlobPath(digestStr)
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create blob directory")
		return c.SendStatus(500)
	}
	if err := os.WriteFile(blobPath, manifestData, 0644); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to save manifest blob")
		return c.SendStatus(500)
	}

	// Parse manifest to determine type and handle accordingly
	var manifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to parse manifest")
		return c.SendStatus(500)
	}

	// Check if this is a manifest list/index (multi-arch image)
	isManifestList := manifest.MediaType == models.MediaTypeOCIManifestList ||
		manifest.MediaType == models.MediaTypeDockerManifestList

	if isManifestList {
		// Handle manifest list/index - preserve raw bytes, don't re-marshal
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":      name,
			"reference": reference,
			"mediaType": manifest.MediaType,
			"size":      len(manifestData),
		}).Debug("Processing manifest list/index")

		// Save manifest to tag-based path (preserving raw bytes)
		manifestPath := h.pathManager.GetImageManifestPath(name, reference)
		if err := h.saveManifestFile(manifestPath, manifestData); err != nil {
			return c.SendStatus(500)
		}

		// Save metadata for image listing (without corrupting the manifest)
		if h.imageService != nil {
			var index models.OCIIndex
			if err := json.Unmarshal(manifestData, &index); err == nil {
				// Calculate approximate total size from manifest descriptors
				var totalSize int64
				for _, m := range index.Manifests {
					totalSize += m.Size
				}
				if err := h.imageService.SaveImageIndex(name, reference, manifestData, totalSize); err != nil {
					h.log.WithFunc().WithError(err).Warn("Failed to save manifest list metadata")
				}
			}
		}
	} else {
		// Handle single-platform manifest
		artifactType := models.DetectArtifactType(&manifest)

		h.log.WithFunc().WithFields(logrus.Fields{
			"name":         name,
			"reference":    reference,
			"artifactType": artifactType,
			"configType":   manifest.Config.MediaType,
		}).Debug("Detected artifact type")

		switch artifactType {
		case models.ArtifactTypeHelmChart:
			if err := h.handleHelmChartManifest(name, reference, &manifest); err != nil {
				h.log.WithFunc().WithError(err).Error("Failed to handle Helm chart")
				return c.SendStatus(500)
			}
			manifestPath := h.pathManager.GetManifestPath(name, reference)
			if err := h.saveManifestFile(manifestPath, manifestData); err != nil {
				return c.SendStatus(500)
			}

		case models.ArtifactTypeDockerImage:
			// Save manifest to tag-based path (preserving raw bytes)
			manifestPath := h.pathManager.GetImageManifestPath(name, reference)
			if err := h.saveManifestFile(manifestPath, manifestData); err != nil {
				return c.SendStatus(500)
			}

			// Save metadata for image listing
			if h.imageService != nil {
				if err := h.imageService.SaveImage(name, reference, &manifest); err != nil {
					h.log.WithFunc().WithError(err).Warn("Failed to save image metadata")
					// Don't fail - manifest is already saved correctly
				}
			}

		default:
			h.log.WithFunc().WithFields(logrus.Fields{
				"configMediaType": manifest.Config.MediaType,
			}).Warn("Unknown artifact type, saving as generic manifest")
			manifestPath := h.pathManager.GetManifestPath(name, reference)
			if err := h.saveManifestFile(manifestPath, manifestData); err != nil {
				return c.SendStatus(500)
			}
		}
	}

	c.Set("Docker-Content-Digest", digestStr)
	// Build absolute URL for Location header (required by OCI clients)
	scheme := "http"
	if c.Protocol() == "https" {
		scheme = "https"
	}
	c.Set("Location", fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, c.Hostname(), name, digestStr))

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
		"digest":    digestStr,
		"size":      len(manifestData),
	}).Info("Manifest saved successfully")

	return c.SendStatus(201)
}

// handleHelmChartManifest processes a Helm chart manifest
func (h *OCIHandler) handleHelmChartManifest(name, reference string, manifest *models.OCIManifest) error {
	var chartDigest string
	for _, layer := range manifest.Layers {
		if layer.MediaType == models.MediaTypeHelmChart {
			chartDigest = layer.Digest
			break
		}
	}

	if chartDigest == "" {
		return fmt.Errorf("helm chart layer not found in manifest")
	}

	chartData, err := h.getBlobByDigest(chartDigest)
	if err != nil {
		return fmt.Errorf("failed to read chart data: %w", err)
	}

	version := reference
	if strings.HasPrefix(reference, "sha256:") {
		metadata, err := h.chartService.ExtractChartMetadata(chartData)
		if err != nil {
			h.log.WithError(err).Warn("Failed to extract chart metadata, using digest")
		} else {
			version = metadata.Version
			h.log.WithFields(logrus.Fields{
				"extractedVersion": version,
				"digest":           reference,
			}).Debug("Extracted chart version from metadata")
		}
	}

	chartName := name
	if idx := strings.LastIndex(name, "/"); idx != -1 {
		chartName = name[idx+1:]
	}
	fileName := fmt.Sprintf("%s-%s.tgz", chartName, version)
	if err := h.chartService.SaveChart(chartData, fileName); err != nil {
		return fmt.Errorf("failed to save chart: %w", err)
	}

	return nil
}

// saveManifestFile saves a manifest to the specified path
func (h *OCIHandler) saveManifestFile(manifestPath string, data []byte) error {
	manifestDir := filepath.Dir(manifestPath)
	if err := os.MkdirAll(manifestDir, 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create manifest directory")
		return err
	}

	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to save manifest")
		return err
	}

	return nil
}
