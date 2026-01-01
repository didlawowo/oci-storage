package handlers

import (
	cfg "oci-storage/config"
	services "oci-storage/pkg/services"
	utils "oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

type BackupHandler struct {
	backupService *services.BackupService
	log           *utils.Logger
	config        *cfg.Config
}

func NewBackupHandler(backupService *services.BackupService, log *utils.Logger, config *cfg.Config) *BackupHandler {
	return &BackupHandler{
		backupService: backupService,
		log:           log,
		config:        config,
	}
}

func (h *BackupHandler) IsBackupEnabled() bool {
	// Vérifier si le backup est activé dans la configuration
	if h.config == nil {
		// Si la config est nil, considérer que le backup est désactivé
		h.log.Warn("⚠️ Backup config is nil, considering backup as disabled")
		return false
	}
	if !h.config.Backup.Enabled {
		return false
	}

	// Vérifier qu'un fournisseur de backup est configuré
	return (h.config.Backup.Provider == "aws" && h.config.Backup.AWS.Bucket != "") ||
		(h.config.Backup.Provider == "gcp" && h.config.Backup.GCP.Bucket != "")
}

func (h *BackupHandler) GetBackupStatus(c *fiber.Ctx) error {
	// Vérification pour éviter les nil pointer dereference
	if h.config == nil {
		h.log.Warn("⚠️ Backup config is nil, returning disabled status")
		return c.JSON(fiber.Map{
			"enabled":  false,
			"provider": "none",
			"message":  "Backup configuration is not available",
		})
	}

	// Maintenant que nous sommes sûrs que config n'est pas nil
	// h.log.Info("✅ Backup status retrieved successfully")
	return c.JSON(fiber.Map{
		"enabled":  h.IsBackupEnabled(),
		"provider": h.config.Backup.Provider,
	})

}

func (h *BackupHandler) HandleBackup(c *fiber.Ctx) error {
	if err := h.backupService.Backup(); err != nil {
		h.log.WithError(err).Error("❌ Backup failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	h.log.Info("✅ Backup successful")
	return c.JSON(fiber.Map{
		"message": "Backup completed successfully",
	})
}

func (h *BackupHandler) HandleRestore(c *fiber.Ctx) error {
	if err := h.backupService.Restore(); err != nil {
		h.log.WithError(err).Error("❌ Restore failed")
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	h.log.Info("✅ Restore successful")
	return c.JSON(fiber.Map{
		"message": "Restore completed successfully",
	})
}
