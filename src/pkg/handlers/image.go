// pkg/handlers/image.go
package handlers

import (
	"helm-portal/pkg/interfaces"
	"helm-portal/pkg/models"
	"helm-portal/pkg/utils"

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
