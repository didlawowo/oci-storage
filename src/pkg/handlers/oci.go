package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/coordination"
	interfaces "oci-storage/pkg/interfaces"
	"oci-storage/pkg/models"
	"oci-storage/pkg/storage"
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
	log           *utils.Logger
	chartService  interfaces.ChartServiceInterface
	imageService  interfaces.ImageServiceInterface
	proxyService  interfaces.ProxyServiceInterface
	scanService   interfaces.ScanServiceInterface
	pathManager   *utils.PathManager
	backend       storage.Backend
	uploadTracker coordination.UploadTracker
	locker        coordination.LockManager
	config        *config.Config
}

func NewOCIHandler(
	chartService interfaces.ChartServiceInterface,
	imageService interfaces.ImageServiceInterface,
	proxyService interfaces.ProxyServiceInterface,
	scanService interfaces.ScanServiceInterface,
	cfg *config.Config,
	log *utils.Logger,
	pm *utils.PathManager,
	backend storage.Backend,
	uploadTracker coordination.UploadTracker,
	locker coordination.LockManager,
) *OCIHandler {
	return &OCIHandler{
		chartService:  chartService,
		imageService:  imageService,
		proxyService:  proxyService,
		scanService:   scanService,
		config:        cfg,
		log:           log,
		pathManager:   pm,
		backend:       backend,
		uploadTracker: uploadTracker,
		locker:        locker,
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
	normalizedName := normalizeDockerHubName(name)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":           name,
		"normalizedName": normalizedName,
		"digest":         digest,
	}).Debug("Processing blob download request")

	// Try local first - stream from backend
	blobPath := h.pathManager.GetBlobPath(digest)
	if exists, _ := h.backend.Exists(blobPath); exists {
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return h.sendBlob(c, blobPath)
	}

	// Not found locally - try proxy if enabled
	if h.proxyService != nil && h.proxyService.IsEnabled() {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":   normalizedName,
			"digest": digest,
		}).Debug("Blob not found locally, trying proxy")

		return h.proxyBlob(c, normalizedName, digest)
	}

	h.log.WithFunc().Debug("Blob not found")
	return c.SendStatus(404)
}

// sendBlob streams a blob from the backend to the client
func (h *OCIHandler) sendBlob(c *fiber.Ctx, path string) error {
	info, err := h.backend.Stat(path)
	if err != nil {
		return c.SendStatus(500)
	}
	reader, err := h.backend.ReadStream(path)
	if err != nil {
		return c.SendStatus(500)
	}
	c.Set("Content-Length", fmt.Sprintf("%d", info.Size))
	// Note: Do NOT defer reader.Close() here. Fiber/fasthttp reads the stream
	// asynchronously after the handler returns. Closing it here would cause EOF.
	// fasthttp will close the reader when it implements io.Closer.
	return c.SendStream(reader, int(info.Size))
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
	normalizedName := normalizeDockerHubName(name)

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

		// Security gate check: verify scan decision before serving manifest
		if blocked, resp := h.checkScanGate(c, manifestData, normalizedName); blocked {
			return resp
		}

		return h.sendManifestResponse(c, manifestData, reference)
	}

	// Not found locally - try proxy if enabled
	// ONLY proxy for paths starting with "proxy/" - this protects charts/ and images/ from being proxied
	// HEAD requests are allowed for proxy paths (needed for container runtime manifest checks)
	isProxyPath := strings.HasPrefix(normalizedName, "proxy/")
	shouldProxy := h.proxyService != nil && h.proxyService.IsEnabled() && isProxyPath

	h.log.WithFunc().WithFields(logrus.Fields{
		"shouldProxy": shouldProxy,
		"isProxyPath": isProxyPath,
		"method":      c.Method(),
	}).Debug("Proxy decision")

	if shouldProxy {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":      normalizedName,
			"reference": reference,
		}).Debug("Manifest not found locally, trying proxy")

		return h.proxyManifest(c, normalizedName, reference)
	}

	h.log.WithFunc().WithError(err).Debug("Manifest not found")
	if c.Method() == "HEAD" {
		return c.Status(404).Send(nil)
	}
	return c.SendStatus(404)
}

// checkScanGate verifies the scan decision for a manifest before serving it.
// Returns (true, response) if the request should be blocked, (false, nil) otherwise.
func (h *OCIHandler) checkScanGate(c *fiber.Ctx, manifestData []byte, name string) (bool, error) {
	if h.scanService == nil || !h.scanService.IsEnabled() {
		return false, nil
	}

	if !h.config.Trivy.Policy.BlockOnPull {
		return false, nil
	}

	// Check exempt images
	for _, exempt := range h.config.Trivy.Policy.ExemptImages {
		if strings.HasPrefix(name, exempt) {
			return false, nil
		}
	}

	// Calculate digest from manifest data
	digestBytes := sha256.Sum256(manifestData)
	digest := fmt.Sprintf("sha256:%x", digestBytes)

	decision, err := h.scanService.GetDecision(digest)
	if err != nil {
		// No decision yet — in warn mode, allow; in block mode, allow (scan may be pending)
		return false, nil
	}

	mode := h.config.Trivy.Policy.Mode
	if mode == "" {
		mode = "warn"
	}

	switch decision.Status {
	case "denied":
		h.log.WithFunc().WithFields(logrus.Fields{
			"digest": digest,
			"name":   name,
			"reason": decision.Reason,
		}).Warn("Image pull denied by security gate")
		return true, c.Status(403).JSON(fiber.Map{
			"errors": []fiber.Map{{
				"code":    "DENIED",
				"message": fmt.Sprintf("Image denied by security policy: %s", decision.Reason),
				"detail": fiber.Map{
					"digest":     digest,
					"scanReport": fmt.Sprintf("/api/scan/report/%s", strings.TrimPrefix(digest, "sha256:")),
				},
			}},
		})

	case "pending":
		if mode == "block" {
			h.log.WithFunc().WithFields(logrus.Fields{
				"digest": digest,
				"name":   name,
			}).Warn("Image pull blocked: awaiting security review")
			return true, c.Status(403).JSON(fiber.Map{
				"errors": []fiber.Map{{
					"code":    "DENIED",
					"message": "Image awaiting security review",
					"detail": fiber.Map{
						"digest":     digest,
						"scanReport": fmt.Sprintf("/api/scan/report/%s", strings.TrimPrefix(digest, "sha256:")),
					},
				}},
			})
		}
		// warn mode: add header but allow
		c.Set("X-Trivy-Warning", "Image has pending security review")
	}

	return false, nil
}

// findManifest searches for a manifest in all possible locations
func (h *OCIHandler) findManifest(name, reference string) ([]byte, string, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return h.findManifestByDigest(name, reference)
	}

	searchPaths := []string{
		h.pathManager.GetManifestPath(name, reference),
		h.pathManager.GetImageManifestPath(name, reference),
	}

	for _, path := range searchPaths {
		if data, err := h.backend.Read(path); err == nil {
			return data, path, nil
		}
	}

	return nil, "", fmt.Errorf("manifest not found for %s:%s", name, reference)
}

// findManifestByDigest searches for a manifest by its digest
func (h *OCIHandler) findManifestByDigest(name, digest string) ([]byte, string, error) {
	// First try blob path (most reliable - stored with correct digest)
	blobPath := h.pathManager.GetBlobPath(digest)
	if data, err := h.backend.Read(blobPath); err == nil {
		return data, blobPath, nil
	}

	// Try finding by filename pattern (sha256_xxx.json) - faster than recalculating hashes
	// This handles cases where manifest was stored with digest in filename
	digestFileName := strings.Replace(digest, ":", "_", 1) + ".json"

	helmManifestsDir := filepath.Join("manifests", name)
	helmManifestPath := filepath.Join(helmManifestsDir, digestFileName)
	if data, err := h.backend.Read(helmManifestPath); err == nil {
		return data, helmManifestPath, nil
	}

	imageManifestsDir := filepath.Join("images", name, "manifests")
	imageManifestPath := filepath.Join(imageManifestsDir, digestFileName)
	if data, err := h.backend.Read(imageManifestPath); err == nil {
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
	entries, err := h.backend.List(dir)
	if err != nil {
		return nil, "", err
	}

	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		filePath := filepath.Join(dir, entry.Name)
		data, err := h.backend.Read(filePath)
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

	chartData, err := h.backend.Read(blobPath)
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
	// Stream body to a temp file while computing digest simultaneously
	tempUUID := generateUUID()
	tempDir := filepath.Dir(h.pathManager.GetTempPath(tempUUID))
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create temp directory")
		return c.SendStatus(500)
	}

	tmpFile, err := os.CreateTemp(tempDir, "blob-upload-*")
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create temp file")
		return c.SendStatus(500)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up temp file on any error path

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	if _, err := io.Copy(writer, c.Request().BodyStream()); err != nil {
		tmpFile.Close()
		h.log.WithFunc().WithError(err).Error("Failed to stream blob upload")
		return c.SendStatus(500)
	}
	tmpFile.Close()

	digest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	blobPath := h.pathManager.GetBlobPath(digest)

	h.log.WithFunc().WithFields(logrus.Fields{
		"digest": digest,
		"path":   blobPath,
	}).Debug("Processing blob upload")

	if err := h.backend.Import(tmpPath, blobPath); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to move blob to final path")
		return c.SendStatus(500)
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

	// Register upload session so other replicas know this pod owns it.
	// TTL of 1 hour covers even very large chunked uploads.
	if err := h.uploadTracker.Register(c.Context(), uuid, 1*time.Hour); err != nil {
		h.log.WithError(err).Warn("Failed to register upload session")
	}

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

	// Check that this upload belongs to this pod (multi-replica safety)
	if err := h.uploadTracker.CheckOwnership(c.Context(), uuid); err != nil {
		h.log.WithError(err).WithField("uuid", uuid).Error("Upload routed to wrong replica")
		return HTTPError(c, 409, fmt.Sprintf("UPLOAD_INVALID: %s - configure session affinity on your load balancer", err.Error()))
	}

	tempPath := h.pathManager.GetTempPath(uuid)

	h.log.WithFunc().WithFields(logrus.Fields{
		"uuid": uuid,
		"path": tempPath,
	}).Debug("Processing PATCH request")

	if err := os.MkdirAll(filepath.Dir(tempPath), 0755); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to create temp directory")
		return c.SendStatus(500)
	}

	// Stream body to disk to handle large blobs without loading into memory
	// Use O_APPEND to support chunked uploads (multiple PATCH requests per upload session)
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to open temp file for PATCH")
		return c.SendStatus(500)
	}

	// Track current offset before writing (for Range response header)
	startOffset, _ := file.Seek(0, io.SeekEnd)

	written, err := io.Copy(file, c.Request().BodyStream())
	file.Close()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to stream body to temp file")
		return c.SendStatus(500)
	}

	if written == 0 {
		h.log.WithFunc().Error("Received empty body")
		return HTTPError(c, 400, "Empty body")
	}

	h.log.WithFunc().WithFields(logrus.Fields{
		"bytes":       written,
		"startOffset": startOffset,
	}).Info("Successfully processed PATCH data")

	// Mark this upload as chunked so CompleteUpload preserves PATCH data (O_APPEND)
	// instead of truncating on retry (O_TRUNC for monolithic uploads)
	chunkedMarker := tempPath + ".chunked"
	if err := os.WriteFile(chunkedMarker, []byte("1"), 0644); err != nil {
		h.log.WithFunc().WithError(err).Warn("Failed to write chunked marker")
	}

	// Build absolute URL for Location header (required by OCI clients like crane)
	scheme := "http"
	if c.Protocol() == "https" {
		scheme = "https"
	}
	location := fmt.Sprintf("%s://%s/v2/%s/blobs/uploads/%s", scheme, c.Hostname(), name, uuid)
	c.Set("Location", location)
	c.Set("Docker-Upload-UUID", uuid)
	c.Set("Range", fmt.Sprintf("0-%d", startOffset+written-1))
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

	// Check that this upload belongs to this pod (multi-replica safety)
	if err := h.uploadTracker.CheckOwnership(c.Context(), uuid); err != nil {
		h.log.WithError(err).WithField("uuid", uuid).Error("Upload routed to wrong replica")
		return HTTPError(c, 409, fmt.Sprintf("UPLOAD_INVALID: %s - configure session affinity on your load balancer", err.Error()))
	}

	tempPath := h.pathManager.GetTempPath(uuid)
	finalPath := h.pathManager.GetBlobPath(digest)
	chunkedMarker := tempPath + ".chunked"

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":      name,
		"uuid":      uuid,
		"digest":    digest,
		"tempPath":  tempPath,
		"finalPath": finalPath,
	}).Debug("Completing upload")

	// Idempotent: if blob already exists at final path, skip processing
	if _, err := h.backend.Stat(finalPath); err == nil {
		h.log.WithFunc().WithField("digest", digest).Info("Blob already exists, skipping upload")
		os.Remove(tempPath)
		os.Remove(chunkedMarker)
		if err := h.uploadTracker.Remove(c.Context(), uuid); err != nil {
			h.log.WithError(err).Debug("Failed to remove upload tracking entry")
		}
		c.Set("Docker-Content-Digest", digest)
		return c.SendStatus(201)
	}

	// Stream any remaining body data to the temp file
	bodyStream := c.Request().BodyStream()
	if bodyStream != nil {
		// Determine open mode: if PATCH chunks were written (.chunked marker exists),
		// append the final chunk. Otherwise use O_TRUNC to handle monolithic upload
		// retries safely (prevents corrupt data from a previous timed-out attempt).
		openFlags := os.O_WRONLY | os.O_CREATE
		if _, err := os.Stat(chunkedMarker); err == nil {
			openFlags |= os.O_APPEND
		} else {
			openFlags |= os.O_TRUNC
		}

		file, err := os.OpenFile(tempPath, openFlags, 0644)
		if err != nil {
			h.log.WithFunc().WithError(err).Error("Failed to open temp file for final data")
			return c.SendStatus(500)
		}
		written, err := io.Copy(file, bodyStream)
		file.Close()
		if err != nil {
			h.log.WithFunc().WithError(err).Error("Failed to stream final data")
			return c.SendStatus(500)
		}
		if written > 0 {
			h.log.WithFunc().WithField("bytes", written).Debug("Wrote final chunk data")
		}
	}

	// Verify the uploaded content matches the declared digest
	actualDigest, err := utils.ComputeFileDigest(tempPath)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to compute digest of uploaded blob")
		os.Remove(tempPath)
		os.Remove(chunkedMarker)
		return c.SendStatus(500)
	}
	if actualDigest != digest {
		h.log.WithFunc().WithFields(logrus.Fields{
			"expected": digest,
			"actual":   actualDigest,
		}).Error("Blob digest mismatch - uploaded content does not match declared digest")
		os.Remove(tempPath)
		os.Remove(chunkedMarker)
		return HTTPError(c, 400, fmt.Sprintf("DIGEST_INVALID: expected %s but got %s", digest, actualDigest))
	}

	if err := h.backend.Import(tempPath, finalPath); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to finalize upload")
		return c.SendStatus(500)
	}

	// Clean up upload session tracking and chunked marker
	os.Remove(chunkedMarker)
	if err := h.uploadTracker.Remove(c.Context(), uuid); err != nil {
		h.log.WithError(err).Debug("Failed to remove upload tracking entry")
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

	info, err := h.backend.Stat(blobPath)
	if err == nil {
		c.Set("Content-Length", fmt.Sprintf("%d", info.Size))
		c.Set("Docker-Content-Digest", digest)
		c.Set("Content-Type", "application/octet-stream")
		return c.SendStatus(200)
	}

	// Blob not found locally - check upstream proxy if enabled
	// Container runtimes (containerd, Docker) do HEAD before GET to check
	// blob existence. Without this, proxy images fail with "blob unknown".
	normalizedName := normalizeDockerHubName(name)
	isProxyPath := strings.HasPrefix(normalizedName, "proxy/")
	if h.proxyService != nil && h.proxyService.IsEnabled() && isProxyPath {
		h.log.WithFunc().WithFields(logrus.Fields{
			"name":   normalizedName,
			"digest": digest,
		}).Debug("Blob not found locally, checking upstream via proxy HEAD")
		return h.proxyHeadBlob(c, normalizedName, digest)
	}
	h.log.WithFunc().Debug("Blob not found locally")
	return c.SendStatus(404)
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

	// Structural validation: ensure the manifest is valid JSON with required OCI fields
	var rawManifest map[string]interface{}
	if err := json.Unmarshal(manifestData, &rawManifest); err != nil {
		h.log.WithFunc().WithError(err).Error("Manifest is not valid JSON")
		return HTTPError(c, 400, "MANIFEST_INVALID: request body is not valid JSON")
	}
	if err := utils.ValidateManifestContent(rawManifest); err != nil {
		h.log.WithFunc().WithError(err).Warn("Manifest structural validation failed")
		return HTTPError(c, 400, err.Error())
	}

	digest := sha256.Sum256(manifestData)
	digestStr := fmt.Sprintf("sha256:%x", digest)

	// Always save manifest to blob storage first (for digest-based lookups)
	// This ensures the exact bytes are preserved and can be retrieved by digest
	blobPath := h.pathManager.GetBlobPath(digestStr)
	if err := h.backend.Write(blobPath, manifestData); err != nil {
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

	// Validate that declared layer/config sizes match actual blob sizes on disk
	// This prevents size mismatches that cause pull failures (e.g. containerd's
	// "failed size validation: X != Y" error)
	if !isManifestList {
		if err := h.validateManifestBlobSizes(name, &manifest); err != nil {
			return HTTPError(c, 400, err.Error())
		}
	}

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
				// Calculate total size by fetching a child manifest and summing its layers
				// m.Size in manifest list is just the manifest size (~1KB), not the image size
				totalSize := h.calculateManifestListSizeFromBlobs(index)
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

	// Trigger async vulnerability scan if enabled
	// Only scan Docker images (not Helm charts), and only for proper tags
	if h.scanService != nil && h.scanService.IsEnabled() &&
		!strings.HasPrefix(reference, "sha256:") && !strings.Contains(reference, "/") {
		if models.DetectArtifactType(&manifest) != models.ArtifactTypeHelmChart {
			h.scanService.ScanImage(name, reference, digestStr)
		}
	}

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

// validateManifestBlobSizes checks that each layer and config blob referenced
// in the manifest exists on disk and that the declared size matches the actual
// file size. This prevents "failed size validation" errors during pull (e.g.
// containerd comparing declared vs actual sizes).
func (h *OCIHandler) validateManifestBlobSizes(name string, manifest *models.OCIManifest) error {
	// Collect all descriptors to validate (config + layers)
	descriptors := append([]models.OCIDescriptor{manifest.Config}, manifest.Layers...)

	for _, desc := range descriptors {
		if desc.Digest == "" {
			continue
		}

		blobPath := h.pathManager.GetBlobPath(desc.Digest)
		info, err := h.backend.Stat(blobPath)
		if err != nil {
			h.log.WithFields(logrus.Fields{
				"name":   name,
				"digest": desc.Digest,
			}).Warn("Manifest references non-existent blob")
			// Don't reject - blob may arrive later in cross-repo mount scenarios
			continue
		}

		if desc.Size > 0 && info.Size != desc.Size {
			h.log.WithFields(logrus.Fields{
				"name":         name,
				"digest":       desc.Digest,
				"declaredSize": desc.Size,
				"actualSize":   info.Size,
			}).Error("Manifest layer size mismatch - declared size does not match blob on disk")
			return fmt.Errorf(
				"BLOB_SIZE_MISMATCH: layer %s declared size %d but actual size is %d",
				desc.Digest, desc.Size, info.Size,
			)
		}
	}

	return nil
}

// saveManifestFile saves a manifest to the specified path via backend
func (h *OCIHandler) saveManifestFile(manifestPath string, data []byte) error {
	if err := h.backend.Write(manifestPath, data); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to save manifest")
		return err
	}
	return nil
}

// calculateManifestListSizeFromBlobs calculates the real size of a manifest list
// by reading a child manifest from local blobs and summing its layers
func (h *OCIHandler) calculateManifestListSizeFromBlobs(index models.OCIIndex) int64 {
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

	// Read the child manifest from local blob storage
	blobPath := h.pathManager.GetBlobPath(targetDigest)
	data, err := h.backend.Read(blobPath)
	if err != nil {
		h.log.WithError(err).WithField("digest", targetDigest).Debug("Could not read child manifest for size calculation")
		return 0
	}

	var childManifest models.OCIManifest
	if err := json.Unmarshal(data, &childManifest); err != nil {
		h.log.WithError(err).Debug("Could not parse child manifest for size calculation")
		return 0
	}

	return childManifest.GetTotalSize()
}
