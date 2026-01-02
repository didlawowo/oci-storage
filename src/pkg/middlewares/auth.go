// pkg/middleware/auth.go
package middleware

import (
	"encoding/base64"
	"oci-storage/config"
	"strings"

	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

type AuthMiddleware struct {
	config *config.Config
	log    *utils.Logger
}

func NewAuthMiddleware(config *config.Config, log *utils.Logger) *AuthMiddleware {
	return &AuthMiddleware{
		config: config,
		log:    log,
	}
}

func (m *AuthMiddleware) Authenticate() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Allow anonymous read access for proxy/cache functionality
		// Only require auth for write operations (PUT, POST, DELETE, PATCH)
		method := c.Method()
		if method == "GET" || method == "HEAD" {
			// Check if auth header is provided - if so, validate it
			auth := c.Get("Authorization")
			if auth == "" {
				// No auth provided, allow anonymous read
				m.log.Debug("Anonymous read access allowed")
				return c.Next()
			}
			// Auth provided, validate it below
		}

		// Récupérer le header d'authentification
		auth := c.Get("Authorization")
		if auth == "" {
			m.log.Warn("No authorization header")
			// Important: Ajouter le header WWW-Authenticate pour le realm
			c.Set("WWW-Authenticate", `Basic realm="Helm Registry"`)
			return c.Status(401).JSON(fiber.Map{
				"errors": []fiber.Map{
					{
						"code":    "UNAUTHORIZED",
						"message": "authentication required",
						"detail":  "basic authentication required",
					},
				},
			})
		}

		// Vérifier le format "Basic base64(username:password)"
		if !strings.HasPrefix(auth, "Basic ") {
			m.log.Warn("Invalid auth format")
			return c.Status(401).SendString("Invalid authentication format")
		}

		// Décoder les credentials
		credentials, err := base64.StdEncoding.DecodeString(auth[6:])
		if err != nil {
			m.log.WithError(err).Warn("Failed to decode credentials")
			return c.Status(401).SendString("Invalid credentials format")
		}

		parts := strings.Split(string(credentials), ":")
		if len(parts) != 2 {
			m.log.Warn("Invalid credentials format")
			return c.Status(401).SendString("Invalid credentials format")
		}

		username, password := parts[0], parts[1]

		// Debug: Log le nombre d'utilisateurs configurés
		m.log.WithField("total_users", len(m.config.Auth.Users)).Debug("Checking authentication")

		// Vérifier les credentials
		for _, user := range m.config.Auth.Users {
			m.log.WithFields(map[string]interface{}{
				"config_user": user.Username,
				"input_user":  username,
			}).Debug("Comparing user")

			if user.Username == username && user.Password == password {
				m.log.WithField("username", username).Info("User authenticated successfully")
				return c.Next()
			}
		}

		m.log.WithField("username", username).Warn("Authentication failed")
		return c.Status(401).JSON(fiber.Map{
			"errors": []fiber.Map{
				{
					"code":    "UNAUTHORIZED",
					"message": "invalid username or password",
				},
			},
		})
	}
}
