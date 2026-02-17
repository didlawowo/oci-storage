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
		// If auth is disabled via config, allow all requests through
		if !m.config.Auth.IsEnabled() {
			return c.Next()
		}

		// Allow anonymous read access for proxy/cache functionality
		// Only require auth for write operations (PUT, POST, DELETE, PATCH)
		//
		// EXCEPTION: GET/HEAD /v2/ (the OCI version check endpoint) must always
		// challenge authentication. This is how "docker login" works:
		//   1. Docker sends GET /v2/ without credentials
		//   2. Registry replies 401 + WWW-Authenticate header
		//   3. Docker re-sends GET /v2/ with Basic auth credentials
		//   4. Registry validates and replies 200
		// Without this, Docker never sends credentials and always reports
		// "Login Succeeded" regardless of username/password.
		method := c.Method()
		if method == "GET" || method == "HEAD" {
			path := c.Path()
			isVersionCheck := path == "/v2" || path == "/v2/"
			if !isVersionCheck {
				auth := c.Get("Authorization")
				if auth == "" {
					m.log.Debug("Anonymous read access allowed")
					return c.Next()
				}
				// Auth header provided on GET/HEAD, validate it below
			}
		}

		// Récupérer le header d'authentification
		auth := c.Get("Authorization")
		if auth == "" {
			m.log.Warn("No authorization header")
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
		decoded, err := base64.StdEncoding.DecodeString(auth[6:])
		if err != nil {
			m.log.WithError(err).Warn("Failed to decode credentials")
			return c.Status(401).SendString("Invalid credentials format")
		}

		// Use SplitN with limit 2 so passwords containing ":" are handled correctly
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			m.log.Warn("Invalid credentials format")
			return c.Status(401).SendString("Invalid credentials format")
		}

		username, password := parts[0], parts[1]

		m.log.WithField("total_users", len(m.config.Auth.Users)).Debug("Checking authentication")

		// Vérifier les credentials
		for _, user := range m.config.Auth.Users {
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
