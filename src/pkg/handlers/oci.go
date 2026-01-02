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

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":   name,
		"digest": digest,
	}).Debug("Processing blob download request")

	// Try local first
	blobData, err := h.getBlobByDigest(digest)
	if err == nil {
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return c.Send(blobData)
	}

	// Not found locally - try proxy if enabled
	if h.proxyService != nil && h.proxyService.IsEnabled() {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":   name,
			"digest": digest,
		}).Debug("Blob not found locally, trying proxy")

		return h.proxyBlob(c, name, digest)
	}

	if os.IsNotExist(err) {
		h.log.WithFunc().WithError(err).Debug("Blob not found")
		return c.SendStatus(404)
	}
	h.log.WithFunc().WithError(err).Error("Failed to retrieve blob")
	return c.SendStatus(500)
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

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
	}).Debug("Processing manifest request")

	// Try to find manifest in local storage first
	manifestData, manifestPath, err := h.findManifest(name, reference)
	if err == nil {
		h.log.WithFunc().WithFields(logrus.Fields{
			"manifestPath": manifestPath,
			"source":       "local",
		}).Debug("Found manifest locally")

		if h.proxyService != nil && h.proxyService.IsEnabled() {
			h.proxyService.UpdateAccessTime(name, reference)
		}

		return h.sendManifestResponse(c, manifestData, reference)
	}

	// Not found locally - try proxy if enabled
	// ONLY proxy for paths starting with "proxy/" AND only for GET requests
	// HEAD requests should never proxy - they're used by push clients to check existence
	isProxyPath := strings.HasPrefix(name, "proxy/")
	shouldProxy := h.proxyService != nil && h.proxyService.IsEnabled() && isProxyPath && c.Method() == "GET"

	h.log.WithFunc().WithFields(logrus.Fields{
		"shouldProxy":     shouldProxy,
		"isProxyPath":     isProxyPath,
		"method":          c.Method(),
	}).Debug("Proxy decision")

	if shouldProxy {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":      name,
			"reference": reference,
		}).Debug("Manifest not found locally, trying proxy")

		return h.proxyManifest(c, name, reference)
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
	helmManifestsDir := filepath.Join(h.pathManager.GetBasePath(), "manifests", name)
	if data, path, err := h.searchDirForDigest(helmManifestsDir, digest); err == nil {
		return data, path, nil
	}

	imageManifestsDir := filepath.Join(h.pathManager.GetBasePath(), "images", name, "manifests")
	if data, path, err := h.searchDirForDigest(imageManifestsDir, digest); err == nil {
		return data, path, nil
	}

	blobPath := h.pathManager.GetBlobPath(digest)
	if data, err := os.ReadFile(blobPath); err == nil {
		return data, blobPath, nil
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

	location := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid)
	c.Set("Location", location)
	c.Set("Docker-Upload-UUID", uuid)
	return c.SendStatus(202)
}

func (h *OCIHandler) PatchBlob(c *fiber.Ctx) error {
	uuid := c.Params("uuid")
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
	c.Set("Range", fmt.Sprintf("0-%d", len(c.Body())-1))
	return c.SendStatus(202)
}

func (h *OCIHandler) CompleteUpload(c *fiber.Ctx) error {
	name := h.getName(c)
	uuid := c.Params("uuid")
	digest := c.Query("digest")

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

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
	}).Debug("Processing manifest upload")

	var manifest models.OCIManifest
	if err := json.Unmarshal(c.Body(), &manifest); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to parse manifest")
		return c.SendStatus(500)
	}

	artifactType := models.DetectArtifactType(&manifest)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":         name,
		"reference":    reference,
		"artifactType": artifactType,
		"configType":   manifest.Config.MediaType,
	}).Debug("Detected artifact type")

	manifestData := c.Body()
	digest := sha256.Sum256(manifestData)
	digestStr := fmt.Sprintf("sha256:%x", digest)

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
		if h.imageService != nil {
			if err := h.imageService.SaveImage(name, reference, &manifest); err != nil {
				h.log.WithFunc().WithError(err).Error("Failed to save Docker image")
				return c.SendStatus(500)
			}
		} else {
			h.log.WithFunc().Warn("Image service not configured, saving manifest only")
			manifestPath := h.pathManager.GetImageManifestPath(name, reference)
			if err := h.saveManifestFile(manifestPath, manifestData); err != nil {
				return c.SendStatus(500)
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

	c.Set("Docker-Content-Digest", digestStr)
	c.Set("Location", fmt.Sprintf("/v2/%s/manifests/%s", name, digestStr))

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":         name,
		"reference":    reference,
		"artifactType": artifactType,
		"digest":       digestStr,
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
		return fmt.Errorf("Helm chart layer not found in manifest")
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
