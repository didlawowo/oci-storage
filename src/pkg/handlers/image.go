// pkg/handlers/image.go
package handlers

import (
	"strings"

	"oci-storage/pkg/interfaces"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

// ImageHandler handles Docker image HTTP requests
type ImageHandler struct {
	log          *utils.Logger
	service      interfaces.ImageServiceInterface
	proxyService interfaces.ProxyServiceInterface
	pathManager  *utils.PathManager
}

// normalizeDockerHubName normalizes Docker Hub image names to include library/ prefix
// This ensures proxy/docker.io/nginx and proxy/docker.io/library/nginx match
// Example: proxy/docker.io/traefik -> proxy/docker.io/library/traefik
func normalizeDockerHubName(name string) string {
	if !strings.Contains(name, "docker.io/") {
		return name
	}

	parts := strings.SplitN(name, "docker.io/", 2)
	if len(parts) != 2 {
		return name
	}

	prefix := parts[0] + "docker.io/"
	imagePart := parts[1]

	// If image doesn't contain "/" it's an official image, add library/
	if !strings.Contains(imagePart, "/") {
		return prefix + "library/" + imagePart
	}

	return name
}

// NewImageHandler creates a new image handler
func NewImageHandler(service interfaces.ImageServiceInterface, proxyService interfaces.ProxyServiceInterface, pathManager *utils.PathManager, log *utils.Logger) *ImageHandler {
	return &ImageHandler{
		service:      service,
		proxyService: proxyService,
		pathManager:  pathManager,
		log:          log,
	}
}

// ListImages returns all Docker images as JSON
func (h *ImageHandler) ListImages(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Listing Docker images")

	images, err := h.service.ListImages()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list images")
		return HTTPError(c, 500, "Failed to list images")
	}

	return c.JSON(fiber.Map{
		"images": images,
	})
}

// HandleImageWildcard handles GET requests for images with deep nested paths
// Supports: /image/name/tag/details or /image/proxy/registry/image/tag/details
func (h *ImageHandler) HandleImageWildcard(c *fiber.Ctx) error {
	// Try both param names for compatibility with different route syntaxes
	path := c.Params("path")
	if path == "" {
		path = c.Params("*")
	}
	// Path format: name/tag/details or proxy/registry/image/tag/details

	// Check if path ends with /details
	if !strings.HasSuffix(path, "/details") {
		// It's a request for tags list: /image/name
		name := path
		return h.getImageTagsInternal(c, name)
	}

	// Remove /details suffix
	path = strings.TrimSuffix(path, "/details")

	// Split to get name and tag - tag is the last segment
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return HTTPError(c, 400, "Invalid image path format")
	}

	name := path[:lastSlash]
	tag := path[lastSlash+1:]

	return h.displayImageDetailsInternal(c, name, tag)
}

// HandleImageDeleteWildcard handles DELETE requests for images with deep nested paths
func (h *ImageHandler) HandleImageDeleteWildcard(c *fiber.Ctx) error {
	// Try both param names for compatibility with different route syntaxes
	path := c.Params("path")
	if path == "" {
		path = c.Params("*")
	}
	// Path format: name/tag

	// Split to get name and tag - tag is the last segment
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return HTTPError(c, 400, "Invalid image path format")
	}

	name := path[:lastSlash]
	tag := path[lastSlash+1:]

	return h.deleteImageInternal(c, name, tag)
}

// getImageTagsInternal is the internal implementation for getting image tags
func (h *ImageHandler) getImageTagsInternal(c *fiber.Ctx, name string) error {
	h.log.WithFunc().WithField("name", name).Debug("Getting image tags")

	tags, err := h.service.ListTags(name)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list tags")
		return HTTPError(c, 500, "Failed to list tags")
	}

	var images []models.ImageMetadata
	for _, tag := range tags {
		metadata, err := h.service.GetImageMetadata(name, tag)
		if err != nil {
			h.log.WithError(err).WithFields(logrus.Fields{
				"name": name,
				"tag":  tag,
			}).Warn("Failed to get image metadata")
			continue
		}
		images = append(images, *metadata)
	}

	return c.JSON(fiber.Map{
		"name":   name,
		"tags":   tags,
		"images": images,
	})
}

// displayImageDetailsInternal is the internal implementation for displaying image details
func (h *ImageHandler) displayImageDetailsInternal(c *fiber.Ctx, name, tag string) error {
	// Normalize Docker Hub names (traefik -> library/traefik)
	normalizedName := normalizeDockerHubName(name)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":           name,
		"normalizedName": normalizedName,
		"tag":            tag,
	}).Debug("Getting image details")

	// Try standard image service first
	metadata, err := h.service.GetImageMetadata(normalizedName, tag)
	if err != nil {
		// Try proxy cache if proxy is enabled
		if h.proxyService != nil && h.proxyService.IsEnabled() {
			cachedImages, cacheErr := h.proxyService.GetCachedImages()
			if cacheErr == nil {
				h.log.WithFunc().WithField("cachedCount", len(cachedImages)).Debug("Searching proxy cache")
				for _, cached := range cachedImages {
					h.log.WithFunc().WithFields(logrus.Fields{
						"cachedName":     cached.Name,
						"cachedTag":      cached.Tag,
						"normalizedName": normalizedName,
						"matchName":      cached.Name == normalizedName,
						"matchTag":       cached.Tag == tag,
					}).Debug("Comparing cached image")
					if cached.Name == normalizedName && cached.Tag == tag {
						// Found in proxy cache - convert to ImageMetadata
						metadata = &models.ImageMetadata{
							Name:       cached.Name,
							Repository: cached.Name,
							Tag:        cached.Tag,
							Digest:     cached.Digest,
							Size:       cached.Size,
							Created:    cached.CachedAt,
						}
						break
					}
				}
			}
		}
		if metadata == nil {
			h.log.WithFunc().WithFields(logrus.Fields{
				"name": name,
				"tag":  tag,
			}).Warn("Image not found in local storage or proxy cache")
			return HTTPError(c, 404, "Image not found")
		}
	}

	config, _ := h.service.GetImageConfig(normalizedName, tag)
	if config != nil {
		metadata.Config = config
	}

	// Return JSON only if explicitly requested (not browser's */*)
	acceptHeader := c.Get("Accept")
	if acceptHeader == "application/json" {
		return c.JSON(metadata)
	}

	return c.Render("image_details", fiber.Map{
		"Title": normalizedName + ":" + tag,
		"Image": metadata,
		"Name":  normalizedName,
		"Tag":   tag,
	})
}

// deleteImageInternal is the internal implementation for deleting an image
func (h *ImageHandler) deleteImageInternal(c *fiber.Ctx, name, tag string) error {
	// Normalize Docker Hub names (traefik -> library/traefik)
	normalizedName := normalizeDockerHubName(name)

	h.log.WithFunc().WithFields(logrus.Fields{
		"name":           name,
		"normalizedName": normalizedName,
		"tag":            tag,
	}).Debug("Deleting image")

	isProxyImage := strings.HasPrefix(normalizedName, "proxy/")

	// For proxy images, use proxy service which handles both cache state AND files
	if isProxyImage && h.proxyService != nil && h.proxyService.IsEnabled() {
		if err := h.proxyService.DeleteCachedImage(normalizedName, tag); err != nil {
			h.log.WithFunc().WithError(err).Error("Failed to delete cached image")
			return HTTPError(c, 500, "Failed to delete image")
		}
		return c.JSON(fiber.Map{
			"message": "Cached image deleted successfully",
			"name":    normalizedName,
			"tag":     tag,
		})
	}

	// For non-proxy images, use standard image service
	if err := h.service.DeleteImage(normalizedName, tag); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete image")
		return HTTPError(c, 500, "Failed to delete image")
	}

	return c.JSON(fiber.Map{
		"message": "Image deleted successfully",
		"name":    normalizedName,
		"tag":     tag,
	})
}
