package handlers

import "github.com/gofiber/fiber/v2"

// HTTPError sends a JSON error response with consistent format
func HTTPError(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(fiber.Map{"error": message})
}
