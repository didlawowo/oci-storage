// pkg/handlers/gc.go
package handlers

import (
	service "oci-storage/pkg/services"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

// GCHandler handles garbage collection HTTP requests
type GCHandler struct {
	gcService *service.GCService
	log       *utils.Logger
}

// NewGCHandler creates a new garbage collection handler
func NewGCHandler(gcService *service.GCService, log *utils.Logger) *GCHandler {
	return &GCHandler{
		gcService: gcService,
		log:       log,
	}
}

// RunGC triggers garbage collection
// POST /gc?dryRun=true|false
func (h *GCHandler) RunGC(c *fiber.Ctx) error {
	dryRun := c.Query("dryRun", "false") == "true"

	h.log.WithField("dryRun", dryRun).Info("GC triggered via API")

	result, err := h.gcService.Run(dryRun)
	if err != nil {
		h.log.WithError(err).Error("GC failed")
		return HTTPError(c, 500, "Garbage collection failed")
	}

	if result == nil {
		return c.Status(409).JSON(fiber.Map{
			"error":   "GC already running",
			"message": "A garbage collection is already in progress",
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"dryRun":  dryRun,
		"result":  result,
	})
}

// GetStats returns storage statistics
// GET /gc/stats
func (h *GCHandler) GetStats(c *fiber.Ctx) error {
	stats, err := h.gcService.GetStats()
	if err != nil {
		h.log.WithError(err).Error("Failed to get storage stats")
		return HTTPError(c, 500, "Failed to get storage statistics")
	}

	return c.JSON(stats)
}
