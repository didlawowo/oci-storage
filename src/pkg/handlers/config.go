// internal/api/handlers/chart_handlers.go

package handlers

import (
	config "oci-storage/config"
	utils "oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

// ChartHandler manages chart operations
type ConfigHandler struct {
	log    *utils.Logger
	config *config.Config
}

func NewConfigHandler(config *config.Config, logger *utils.Logger) *ConfigHandler {

	return &ConfigHandler{
		config: config,
		log:    logger,
	}
}

func (h *ConfigHandler) GetConfig(c *fiber.Ctx) error {
	return c.JSON(h.config)
}
