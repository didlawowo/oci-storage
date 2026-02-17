package middleware

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"oci-storage/config"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLogger() *utils.Logger {
	return utils.NewLogger(utils.Config{
		LogLevel:  "error",
		LogFormat: "json",
		Pretty:    false,
	})
}

func boolPtr(b bool) *bool { return &b }

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// setupOCIApp creates a Fiber app with the /v2 group and auth middleware,
// matching the real routing in main.go.
func setupOCIApp(cfg *config.Config) *fiber.App {
	log := newTestLogger()
	m := NewAuthMiddleware(cfg, log)

	app := fiber.New()
	v2 := app.Group("/v2")
	v2.Use(m.Authenticate())

	// Version check endpoint (same as main.go)
	v2.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"apiVersion": "2.0"})
	})

	// Simulated manifest read (anonymous read should work)
	v2.Get("/:name/manifests/:ref", func(c *fiber.Ctx) error {
		return c.SendString("manifest-data")
	})

	// Simulated manifest write (always requires auth)
	v2.Put("/:name/manifests/:ref", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	return app
}

func defaultAuthConfig() *config.Config {
	return &config.Config{
		Auth: config.AuthConfig{
			Users: []config.User{
				{Username: "admin", Password: "admin123"},
				{Username: "user", Password: "p@ss:word"}, // password with colon
			},
		},
	}
}

// --- Tests for GET /v2/ (version check / docker login) ---

func TestVersionCheck_NoAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
	assert.Equal(t, `Basic realm="Helm Registry"`, resp.Header.Get("WWW-Authenticate"))

	var body map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)
	errors := body["errors"].([]interface{})
	assert.Len(t, errors, 1)
	first := errors[0].(map[string]interface{})
	assert.Equal(t, "UNAUTHORIZED", first["code"])
}

func TestVersionCheck_ValidAuth_Returns200(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", basicAuth("admin", "admin123"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestVersionCheck_InvalidAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", basicAuth("admin", "wrong"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)

	var body map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)
	errors := body["errors"].([]interface{})
	first := errors[0].(map[string]interface{})
	assert.Equal(t, "invalid username or password", first["message"])
}

func TestVersionCheck_HeadNoAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("HEAD", "/v2/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// --- Tests for anonymous read on non-version-check endpoints ---

func TestManifestGet_NoAuth_Returns200(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/myapp/manifests/v1.0", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "manifest-data", string(body))
}

func TestManifestGet_InvalidAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	// If auth is provided on a GET, it must be valid
	req := httptest.NewRequest("GET", "/v2/myapp/manifests/v1.0", nil)
	req.Header.Set("Authorization", basicAuth("admin", "wrong"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestManifestGet_ValidAuth_Returns200(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/myapp/manifests/v1.0", nil)
	req.Header.Set("Authorization", basicAuth("admin", "admin123"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

// --- Tests for write operations (always require auth) ---

func TestManifestPut_NoAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/v1.0", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
	assert.Equal(t, `Basic realm="Helm Registry"`, resp.Header.Get("WWW-Authenticate"))
}

func TestManifestPut_ValidAuth_Returns200(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/v1.0", nil)
	req.Header.Set("Authorization", basicAuth("admin", "admin123"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestManifestPut_InvalidAuth_Returns401(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/v1.0", nil)
	req.Header.Set("Authorization", basicAuth("admin", "wrong"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// --- Tests for auth disabled ---

func TestAuthDisabled_AllRequestsPass(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Enabled: boolPtr(false),
			Users:   []config.User{{Username: "admin", Password: "admin123"}},
		},
	}
	app := setupOCIApp(cfg)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"GET /v2/ no auth", "GET", "/v2/"},
		{"PUT manifest no auth", "PUT", "/v2/myapp/manifests/v1.0"},
		{"GET manifest no auth", "GET", "/v2/myapp/manifests/v1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			assert.Equal(t, 200, resp.StatusCode)
		})
	}
}

// --- Tests for password with colon ---

func TestPasswordWithColon(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	// user "user" has password "p@ss:word" (contains colon)
	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", basicAuth("user", "p@ss:word"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestPasswordWithColon_WrongPassword(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	req := httptest.NewRequest("GET", "/v2/", nil)
	req.Header.Set("Authorization", basicAuth("user", "p@ss:wrong"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// --- Tests for malformed auth headers ---

func TestMalformedAuth(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	tests := []struct {
		name   string
		header string
		status int
	}{
		{"Bearer instead of Basic", "Bearer token123", 401},
		{"Invalid base64", "Basic not-valid-base64!", 401},
		{"Empty username", "Basic " + base64.StdEncoding.EncodeToString([]byte(":password")), 401},
		{"No colon separator", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon")), 401},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/v2/myapp/manifests/v1.0", nil)
			req.Header.Set("Authorization", tt.header)
			resp, err := app.Test(req)
			require.NoError(t, err)
			assert.Equal(t, tt.status, resp.StatusCode)
		})
	}
}

// --- Docker login simulation ---

func TestDockerLoginFlow_ValidCredentials(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	// Step 1: Docker sends GET /v2/ without auth
	req1 := httptest.NewRequest("GET", "/v2/", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	assert.Equal(t, 401, resp1.StatusCode, "should challenge with 401")
	assert.Contains(t, resp1.Header.Get("WWW-Authenticate"), "Basic")

	// Step 2: Docker re-sends GET /v2/ with valid credentials
	req2 := httptest.NewRequest("GET", "/v2/", nil)
	req2.Header.Set("Authorization", basicAuth("admin", "admin123"))
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	assert.Equal(t, 200, resp2.StatusCode, "should succeed with valid credentials")
}

func TestDockerLoginFlow_InvalidCredentials(t *testing.T) {
	app := setupOCIApp(defaultAuthConfig())

	// Step 1: Docker sends GET /v2/ without auth
	req1 := httptest.NewRequest("GET", "/v2/", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	assert.Equal(t, 401, resp1.StatusCode)

	// Step 2: Docker re-sends with wrong credentials
	req2 := httptest.NewRequest("GET", "/v2/", nil)
	req2.Header.Set("Authorization", basicAuth("admin", "wrongpassword"))
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	assert.Equal(t, 401, resp2.StatusCode, "should reject invalid credentials")
}

func TestDockerLoginFlow_AuthDisabled(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Enabled: boolPtr(false),
		},
	}
	app := setupOCIApp(cfg)

	// When auth is disabled, GET /v2/ should pass without credentials
	req := httptest.NewRequest("GET", "/v2/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}
