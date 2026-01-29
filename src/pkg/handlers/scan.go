package handlers

import (
	"oci-storage/pkg/interfaces"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

// ScanHandler handles vulnerability scan API endpoints
type ScanHandler struct {
	scanService interfaces.ScanServiceInterface
	log         *utils.Logger
}

// NewScanHandler creates a new ScanHandler
func NewScanHandler(scanService interfaces.ScanServiceInterface, log *utils.Logger) *ScanHandler {
	return &ScanHandler{
		scanService: scanService,
		log:         log,
	}
}

// GetPending returns all images awaiting security review
func (h *ScanHandler) GetPending(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Listing pending scan decisions")

	pending, err := h.scanService.ListPendingDecisions()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list pending decisions")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to list pending decisions"})
	}

	return c.JSON(pending)
}

// GetReport returns the scan report for a specific digest
func (h *ScanHandler) GetReport(c *fiber.Ctx) error {
	digest := c.Params("digest")
	if digest == "" {
		return c.Status(400).JSON(fiber.Map{"error": "digest is required"})
	}

	// Reconstruct full digest (URL param uses - instead of :)
	fullDigest := "sha256:" + digest

	result, err := h.scanService.GetScanResult(fullDigest)
	if err != nil {
		h.log.WithFunc().WithError(err).WithField("digest", fullDigest).Debug("Scan result not found")
		return c.Status(404).JSON(fiber.Map{"error": "Scan result not found"})
	}

	// Also attach decision if available
	decision, _ := h.scanService.GetDecision(fullDigest)

	return c.JSON(fiber.Map{
		"scanResult": result,
		"decision":   decision,
	})
}

// Approve approves an image for pulling
func (h *ScanHandler) Approve(c *fiber.Ctx) error {
	digest := c.Params("digest")
	if digest == "" {
		return c.Status(400).JSON(fiber.Map{"error": "digest is required"})
	}

	fullDigest := "sha256:" + digest

	var body struct {
		Reason        string `json:"reason"`
		DecidedBy     string `json:"decidedBy"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if err := c.BodyParser(&body); err != nil {
		// Allow empty body with defaults
		body.Reason = "Approved by admin"
		body.DecidedBy = "admin"
	}
	if body.DecidedBy == "" {
		body.DecidedBy = "admin"
	}
	if body.Reason == "" {
		body.Reason = "Approved by admin"
	}

	if err := h.scanService.SetDecision(fullDigest, "approved", body.Reason, body.DecidedBy, body.ExpiresInDays); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to approve image")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to approve image"})
	}

	h.log.WithFunc().WithField("digest", fullDigest).Info("Image approved")
	return c.JSON(fiber.Map{"status": "approved", "digest": fullDigest})
}

// Deny denies an image from being pulled
func (h *ScanHandler) Deny(c *fiber.Ctx) error {
	digest := c.Params("digest")
	if digest == "" {
		return c.Status(400).JSON(fiber.Map{"error": "digest is required"})
	}

	fullDigest := "sha256:" + digest

	var body struct {
		Reason    string `json:"reason"`
		DecidedBy string `json:"decidedBy"`
	}
	if err := c.BodyParser(&body); err != nil {
		body.Reason = "Denied by admin"
		body.DecidedBy = "admin"
	}
	if body.DecidedBy == "" {
		body.DecidedBy = "admin"
	}
	if body.Reason == "" {
		body.Reason = "Denied by admin"
	}

	if err := h.scanService.SetDecision(fullDigest, "denied", body.Reason, body.DecidedBy, 0); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to deny image")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to deny image"})
	}

	h.log.WithFunc().WithField("digest", fullDigest).Info("Image denied")
	return c.JSON(fiber.Map{"status": "denied", "digest": fullDigest})
}

// DeleteDecision removes a decision, forcing re-review
func (h *ScanHandler) DeleteDecision(c *fiber.Ctx) error {
	digest := c.Params("digest")
	if digest == "" {
		return c.Status(400).JSON(fiber.Map{"error": "digest is required"})
	}

	fullDigest := "sha256:" + digest

	if err := h.scanService.DeleteDecision(fullDigest); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete decision")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to delete decision"})
	}

	h.log.WithFunc().WithField("digest", fullDigest).Info("Decision deleted")
	return c.JSON(fiber.Map{"status": "deleted", "digest": fullDigest})
}

// GetSummary returns aggregate scan statistics
func (h *ScanHandler) GetSummary(c *fiber.Ctx) error {
	summary, err := h.scanService.GetSummary()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to get scan summary")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to get scan summary"})
	}

	return c.JSON(summary)
}

// ListAll returns all scan decisions
func (h *ScanHandler) ListAll(c *fiber.Ctx) error {
	decisions, err := h.scanService.ListAllDecisions()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list decisions")
		return c.Status(500).JSON(fiber.Map{"error": "Failed to list decisions"})
	}

	return c.JSON(decisions)
}
