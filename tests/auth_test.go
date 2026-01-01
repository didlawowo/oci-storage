package tests

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"oci-storage/config"
	middleware "oci-storage/pkg/middlewares"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAuthTest configure un environnement de test pour l'authentification
func setupAuthTest() (*fiber.App, *middleware.AuthMiddleware) {
	// Configuration de test avec des utilisateurs
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Users: []config.User{
				{Username: "admin", Password: "admin123"},
				{Username: "user", Password: "password"},
				{Username: "test", Password: "test123"},
			},
		},
	}

	// Logger de test
	log := utils.NewLogger(utils.Config{
		LogLevel:  "debug",
		LogFormat: "json",
		Pretty:    true,
	})

	// Créer le middleware d'authentification
	authMiddleware := middleware.NewAuthMiddleware(cfg, log)

	// App Fiber de test
	app := fiber.New()

	return app, authMiddleware
}

// createBasicAuthHeader crée un header d'authentification Basic
func createBasicAuthHeader(username, password string) string {
	credentials := username + ":" + password
	encoded := base64.StdEncoding.EncodeToString([]byte(credentials))
	return "Basic " + encoded
}

func TestAuthMiddleware_ValidCredentials(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	tests := []struct {
		name     string
		username string
		password string
	}{
		{
			name:     "Admin user valid credentials",
			username: "admin",
			password: "admin123",
		},
		{
			name:     "Regular user valid credentials",
			username: "user",
			password: "password",
		},
		{
			name:     "Test user valid credentials",
			username: "test",
			password: "test123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Créer la requête avec l'authentification
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", createBasicAuthHeader(tt.username, tt.password))

			// Exécuter la requête
			resp, err := app.Test(req)

			// Vérifications
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)

			// Vérifier le corps de la réponse
			body := make([]byte, 7)
			resp.Body.Read(body)
			assert.Equal(t, "success", string(body))
		})
	}
}

func TestAuthMiddleware_InvalidCredentials(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	tests := []struct {
		name           string
		username       string
		password       string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "Wrong password",
			username:       "admin",
			password:       "wrongpassword",
			expectedStatus: 401,
			expectedError:  "invalid username or password",
		},
		{
			name:           "Non-existent user",
			username:       "nonexistent",
			password:       "anypassword",
			expectedStatus: 401,
			expectedError:  "invalid username or password",
		},
		{
			name:           "Empty username",
			username:       "",
			password:       "password",
			expectedStatus: 401,
			expectedError:  "invalid username or password",
		},
		{
			name:           "Empty password",
			username:       "admin",
			password:       "",
			expectedStatus: 401,
			expectedError:  "invalid username or password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Créer la requête avec des credentials invalides
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", createBasicAuthHeader(tt.username, tt.password))

			// Exécuter la requête
			resp, err := app.Test(req)

			// Vérifications
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)

			// Vérifier la structure de la réponse JSON d'erreur
			var errorResponse map[string]interface{}
			err = json.NewDecoder(resp.Body).Decode(&errorResponse)
			require.NoError(t, err)

			// Vérifier la présence du champ errors
			errors, exists := errorResponse["errors"]
			assert.True(t, exists)

			// Vérifier le message d'erreur
			errorsList := errors.([]interface{})
			assert.Len(t, errorsList, 1)

			firstError := errorsList[0].(map[string]interface{})
			assert.Equal(t, tt.expectedError, firstError["message"])
		})
	}
}

func TestAuthMiddleware_MissingAuthHeader(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	// Créer la requête sans header d'authentification
	req := httptest.NewRequest("GET", "/protected", nil)

	// Exécuter la requête
	resp, err := app.Test(req)

	// Vérifications
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)

	// Vérifier le header WWW-Authenticate
	wwwAuth := resp.Header.Get("WWW-Authenticate")
	assert.Equal(t, `Basic realm="Helm Registry"`, wwwAuth)

	// Vérifier la structure de la réponse JSON d'erreur
	var errorResponse map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&errorResponse)
	require.NoError(t, err)

	// Vérifier la présence du champ errors
	errors, exists := errorResponse["errors"]
	assert.True(t, exists)

	errorsList := errors.([]interface{})
	assert.Len(t, errorsList, 1)

	firstError := errorsList[0].(map[string]interface{})
	assert.Equal(t, "UNAUTHORIZED", firstError["code"])
	assert.Equal(t, "authentication required", firstError["message"])
	assert.Equal(t, "basic authentication required", firstError["detail"])
}

func TestAuthMiddleware_InvalidAuthFormat(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	tests := []struct {
		name       string
		authHeader string
	}{
		{
			name:       "Bearer token instead of Basic",
			authHeader: "Bearer token123",
		},
		{
			name:       "Invalid base64 encoding",
			authHeader: "Basic invalid-base64",
		},
		{
			name:       "Missing colon separator",
			authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin")),
		},
		{
			name:       "Multiple colons",
			authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:pass:extra")),
		},
		{
			name:       "Empty credentials",
			authHeader: "Basic " + base64.StdEncoding.EncodeToString([]byte("")),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Créer la requête avec un format d'authentification invalide
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", tt.authHeader)

			// Exécuter la requête
			resp, err := app.Test(req)

			// Vérifications
			require.NoError(t, err)
			assert.Equal(t, 401, resp.StatusCode)
		})
	}
}

func TestAuthMiddleware_CaseSensitivity(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	tests := []struct {
		name           string
		username       string
		password       string
		expectedStatus int
	}{
		{
			name:           "Username case sensitivity",
			username:       "ADMIN", // Majuscules
			password:       "admin123",
			expectedStatus: 401, // Devrait échouer car case-sensitive
		},
		{
			name:           "Password case sensitivity",
			username:       "admin",
			password:       "ADMIN123", // Majuscules
			expectedStatus: 401,        // Devrait échouer car case-sensitive
		},
		{
			name:           "Correct case",
			username:       "admin",
			password:       "admin123",
			expectedStatus: 200, // Devrait réussir
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Créer la requête
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", createBasicAuthHeader(tt.username, tt.password))

			// Exécuter la requête
			resp, err := app.Test(req)

			// Vérifications
			require.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, resp.StatusCode)
		})
	}
}

func TestAuthMiddleware_MultipleUsers(t *testing.T) {
	app, authMiddleware := setupAuthTest()

	// Route protégée de test
	app.Get("/protected", authMiddleware.Authenticate(), func(c *fiber.Ctx) error {
		return c.SendString("success")
	})

	// Test que tous les utilisateurs configurés peuvent s'authentifier
	users := []struct {
		username string
		password string
	}{
		{"admin", "admin123"},
		{"user", "password"},
		{"test", "test123"},
	}

	for _, user := range users {
		t.Run("User: "+user.username, func(t *testing.T) {
			// Créer la requête avec les credentials de l'utilisateur
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", createBasicAuthHeader(user.username, user.password))

			// Exécuter la requête
			resp, err := app.Test(req)

			// Vérifications
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)

			// Vérifier le corps de la réponse
			body := make([]byte, 7)
			resp.Body.Read(body)
			assert.Equal(t, "success", string(body))
		})
	}
}
