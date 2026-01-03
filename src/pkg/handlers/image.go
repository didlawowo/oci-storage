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
	log         *utils.Logger
	service     interfaces.ImageServiceInterface
	pathManager *utils.PathManager
}

// NewImageHandler creates a new image handler
func NewImageHandler(service interfaces.ImageServiceInterface, pathManager *utils.PathManager, log *utils.Logger) *ImageHandler {
	return &ImageHandler{
		service:     service,
		pathManager: pathManager,
		log:         log,
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

// GetImageTags returns all tags for a specific image
func (h *ImageHandler) GetImageTags(c *fiber.Ctx) error {
	name := c.Params("name")

	h.log.WithFunc().WithField("name", name).Debug("Getting image tags")

	tags, err := h.service.ListTags(name)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list tags")
		return HTTPError(c, 500, "Failed to list tags")
	}

	// Get metadata for each tag
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

// DisplayImageDetails displays details for a specific image tag
func (h *ImageHandler) DisplayImageDetails(c *fiber.Ctx) error {
	name := c.Params("name")
	tag := c.Params("tag")

	h.log.WithFunc().WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Debug("Getting image details")

	metadata, err := h.service.GetImageMetadata(name, tag)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Image not found")
		return HTTPError(c, 404, "Image not found")
	}

	// Try to get config for more details
	config, _ := h.service.GetImageConfig(name, tag)
	if config != nil {
		metadata.Config = config
	}

	// Check Accept header for JSON vs HTML
	if c.Accepts("application/json") != "" {
		return c.JSON(metadata)
	}

	// Return HTML view
	return c.Render("image_details", fiber.Map{
		"Title": name + ":" + tag,
		"Image": metadata,
		"Name":  name,
		"Tag":   tag,
	})
}

// DeleteImage deletes a Docker image by name and tag
func (h *ImageHandler) DeleteImage(c *fiber.Ctx) error {
	name := c.Params("name")
	tag := c.Params("tag")

	h.log.WithFunc().WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Debug("Deleting image")

	if err := h.service.DeleteImage(name, tag); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete image")
		return HTTPError(c, 500, "Failed to delete image")
	}

	return c.JSON(fiber.Map{
		"message": "Image deleted successfully",
		"name":    name,
		"tag":     tag,
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
	h.log.WithFunc().WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Debug("Getting image details")

	metadata, err := h.service.GetImageMetadata(name, tag)
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Image not found")
		return HTTPError(c, 404, "Image not found")
	}

	config, _ := h.service.GetImageConfig(name, tag)
	if config != nil {
		metadata.Config = config
	}

	if c.Accepts("application/json") != "" {
		return c.JSON(metadata)
	}

	return c.Render("image_details", fiber.Map{
		"Title": name + ":" + tag,
		"Image": metadata,
		"Name":  name,
		"Tag":   tag,
	})
}

// deleteImageInternal is the internal implementation for deleting an image
func (h *ImageHandler) deleteImageInternal(c *fiber.Ctx, name, tag string) error {
	h.log.WithFunc().WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Debug("Deleting image")

	if err := h.service.DeleteImage(name, tag); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete image")
		return HTTPError(c, 500, "Failed to delete image")
	}

	return c.JSON(fiber.Map{
		"message": "Image deleted successfully",
		"name":    name,
		"tag":     tag,
	})
}
