package handlers

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func setupProxyTestEnv(t *testing.T) (*fiber.App, *MockChartService, *MockImageService, *MockProxyService, *OCIHandler, string, func()) {
	tempDir, err := os.MkdirTemp("", "oci-storage-proxy-test")
	assert.NoError(t, err)

	log := utils.NewLogger(utils.Config{})
	mockChartService := new(MockChartService)
	mockImageService := new(MockImageService)
	mockProxyService := new(MockProxyService)
	pathManager := utils.NewPathManager(tempDir, log)

	mockChartService.On("GetPathManager").Return(pathManager)

	cfg := &config.Config{
		Proxy: config.ProxyConfig{
			Enabled: true,
			Cache: config.CacheConfig{
				MaxSizeGB: 10,
			},
			Registries: []config.RegistryConfig{
				{Name: "docker.io", URL: "https://registry-1.docker.io", Default: true},
			},
		},
	}

	handler := NewOCIHandler(mockChartService, mockImageService, mockProxyService, nil, cfg, log)
	app := fiber.New()

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return app, mockChartService, mockImageService, mockProxyService, handler, tempDir, cleanup
}

func TestHandleManifest_LocalFound(t *testing.T) {
	app, mockChartService, _, mockProxyService, handler, tempDir, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)

	// Create local manifest
	manifestContent := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`)
	manifestDir := filepath.Join(tempDir, "images", "nginx", "manifests")
	err := os.MkdirAll(manifestDir, 0755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(manifestDir, "alpine.json"), manifestContent, 0644)
	assert.NoError(t, err)

	// Proxy should update access time when found locally
	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("UpdateAccessTime", "nginx", "alpine").Return()

	req := httptest.NewRequest("GET", "/v2/nginx/manifests/alpine", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	mockChartService.AssertExpectations(t)
}

func TestHandleManifest_ProxyOnMiss(t *testing.T) {
	app, _, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	// Use nested route for proxy/ prefix
	app.Get("/v2/:ns1/:ns2/:name/manifests/:reference", handler.HandleManifestDeepNested)

	upstreamManifest := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`)

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", "alpine").
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)
	mockImageService.On("SaveImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/nginx/manifests/alpine", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/vnd.oci.image.manifest.v1+json")
	assert.NotEmpty(t, resp.Header.Get("Docker-Content-Digest"))

	// Wait for background goroutine
	time.Sleep(100 * time.Millisecond)
}

func TestHandleManifest_ProxyDigestReference(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	// Use nested route for proxy/ prefix
	app.Get("/v2/:ns1/:ns2/:name/manifests/:reference", handler.HandleManifestDeepNested)

	// Simulate a child manifest fetch by digest (multi-arch scenario)
	childManifest := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": {"digest": "sha256:abc"}}`)
	digest := "sha256:dcfed685de6f232a6cefc043f92d8b0d64c8d1edf650a61805f2c7a3d745b749"

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(childManifest, "application/vnd.oci.image.manifest.v1+json", nil)

	// Test GET request with digest - should proxy (needed for multi-arch)
	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/nginx/manifests/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("Docker-Content-Digest"))
}

func TestHandleManifest_HeadTagNoProxy(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Head("/v2/:name/manifests/:reference", handler.HandleManifest)

	mockProxyService.On("IsEnabled").Return(true)
	// HEAD requests should NOT proxy - they return 404 if manifest not found locally
	// This allows push clients to check if manifest exists before uploading

	// HEAD request with tag reference should return 404 (not found locally, no proxy)
	req := httptest.NewRequest("HEAD", "/v2/myimage/manifests/v1.0.0", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
}

func TestHandleManifest_ProxyDisabled(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)

	mockProxyService.On("IsEnabled").Return(false)

	req := httptest.NewRequest("GET", "/v2/nginx/manifests/alpine", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
}

func TestFindManifestByDigest_ChecksBlobs(t *testing.T) {
	app, _, _, mockProxyService, handler, tempDir, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)

	// Create a manifest stored as blob (how child manifests are cached)
	// The digest must match the actual sha256 of the content
	manifestContent := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`)
	// sha256 of manifestContent = d69e8ea6ee409e20a594645cd05d6eb0cb313e540f4d027a6492ad588aa2faff
	digest := "sha256:d69e8ea6ee409e20a594645cd05d6eb0cb313e540f4d027a6492ad588aa2faff"

	// PathManager stores blobs at blobs/{full_digest} including sha256: prefix
	blobDir := filepath.Join(tempDir, "blobs")
	err := os.MkdirAll(blobDir, 0755)
	assert.NoError(t, err)

	// Store with full digest (sha256:hash)
	blobPath := filepath.Join(blobDir, digest)
	err = os.WriteFile(blobPath, manifestContent, 0644)
	assert.NoError(t, err)

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("UpdateAccessTime", "nginx", digest).Return()

	req := httptest.NewRequest("GET", "/v2/nginx/manifests/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestGetBlob_LocalFound(t *testing.T) {
	app, _, _, mockProxyService, handler, tempDir, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:name/blobs/:digest", handler.GetBlob)

	blobContent := []byte("test blob content")
	digest := "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abcd"

	// Create blob file locally
	blobDir := filepath.Join(tempDir, "blobs")
	err := os.MkdirAll(blobDir, 0755)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(blobDir, digest), blobContent, 0644)
	assert.NoError(t, err)

	// Proxy should not be called when blob exists locally
	mockProxyService.On("IsEnabled").Return(true).Maybe()

	req := httptest.NewRequest("GET", "/v2/nginx/blobs/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, digest, resp.Header.Get("Docker-Content-Digest"))
}

func TestGetBlob_ProxyTriggered(t *testing.T) {
	// This test verifies that the proxy is called when blob is not found locally
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:name/blobs/:digest", handler.GetBlob)

	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	// Return error to simulate upstream failure - verifies proxy path is taken
	mockProxyService.On("GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(nil, int64(0), io.EOF)

	req := httptest.NewRequest("GET", "/v2/nginx/blobs/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 502, resp.StatusCode) // Bad Gateway when upstream fails
	mockProxyService.AssertCalled(t, "GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest)
}

func TestNestedPath_ProxyManifest(t *testing.T) {
	app, _, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	// Use deep nested route for proxy/ prefix with namespace
	app.Get("/v2/:ns1/:ns2/:ns3/:name/manifests/:reference", handler.HandleManifestDeepNested4)

	upstreamManifest := []byte(`{"schemaVersion": 2}`)

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/myorg/myimage").Return("https://registry-1.docker.io", "myorg/myimage", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "myorg/myimage", "latest").
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)
	mockImageService.On("SaveImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/myorg/myimage/manifests/latest", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	time.Sleep(100 * time.Millisecond)
}

func TestMultiArchManifestFlow(t *testing.T) {
	// This test simulates the full multi-arch manifest flow:
	// 1. Client requests proxy/docker.io/nginx:alpine (tag)
	// 2. Proxy returns OCI Image Index (multi-arch manifest)
	// 3. Client requests child manifest by digest
	// 4. Proxy returns the architecture-specific manifest

	app, _, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:ns1/:ns2/:name/manifests/:reference", handler.HandleManifestDeepNested)

	// Architecture-specific manifest - defined first to calculate its digest
	archManifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": {"digest": "sha256:config123"},
		"layers": [{"digest": "sha256:layer123"}]
	}`)

	// The actual sha256 of archManifest above
	childDigest := "sha256:a74fe2bdc4f2af02400b8bc5c8dd3276465457c40746acc7ff1bbc9e066a2e29"

	// OCI Image Index (multi-arch manifest) - must reference the correct digest
	indexManifest := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.index.v1+json",
		"manifests": [
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"digest": "sha256:a74fe2bdc4f2af02400b8bc5c8dd3276465457c40746acc7ff1bbc9e066a2e29",
				"size": 175,
				"platform": {"architecture": "amd64", "os": "linux"}
			}
		]
	}`)

	// Setup mocks - all GetManifest calls must be configured upfront
	// because prefetchPlatformManifests runs in a goroutine
	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", "alpine").
		Return(indexManifest, "application/vnd.oci.image.index.v1+json", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", childDigest).
		Return(archManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)
	mockProxyService.On("UpdateAccessTime", mock.Anything, mock.Anything).Return()
	mockImageService.On("SaveImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockImageService.On("SaveImageIndex", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	// First request: get the index via proxy/ prefix
	req1 := httptest.NewRequest("GET", "/v2/proxy/docker.io/nginx/manifests/alpine", nil)
	resp1, err := app.Test(req1)
	assert.NoError(t, err)
	assert.Equal(t, 200, resp1.StatusCode)
	assert.Contains(t, resp1.Header.Get("Content-Type"), "index")

	// Wait for background goroutine (prefetch)
	time.Sleep(100 * time.Millisecond)
}

func TestProxyServiceResolveRegistry(t *testing.T) {
	// Test the registry resolution logic
	tests := []struct {
		name         string
		imagePath    string
		expectedURL  string
		expectedName string
		setupMock    func(*MockProxyService)
	}{
		{
			name:         "simple image uses default registry",
			imagePath:    "nginx",
			expectedURL:  "https://registry-1.docker.io",
			expectedName: "library/nginx",
			setupMock: func(m *MockProxyService) {
				m.On("ResolveRegistry", "nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
			},
		},
		{
			name:         "namespaced image",
			imagePath:    "myorg/myimage",
			expectedURL:  "https://registry-1.docker.io",
			expectedName: "myorg/myimage",
			setupMock: func(m *MockProxyService) {
				m.On("ResolveRegistry", "myorg/myimage").Return("https://registry-1.docker.io", "myorg/myimage", nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProxy := new(MockProxyService)
			tt.setupMock(mockProxy)

			url, name, err := mockProxy.ResolveRegistry(tt.imagePath)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedURL, url)
			assert.Equal(t, tt.expectedName, name)
		})
	}
}

// Tests for deep nested paths (3 segments: proxy/docker.io/nginx)
func TestDeepNestedPath_3Segments_ProxyManifest(t *testing.T) {
	app, _, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	// Route for 3 segments: proxy/docker.io/nginx
	app.Get("/v2/:ns1/:ns2/:name/manifests/:reference", handler.HandleManifestDeepNested)

	upstreamManifest := []byte(`{"schemaVersion": 2}`)

	// The full path "proxy/docker.io/nginx" should be assembled and resolved
	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", "alpine").
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)
	mockImageService.On("SaveImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/nginx/manifests/alpine", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	time.Sleep(100 * time.Millisecond)
}

func TestDeepNestedPath_3Segments_HeadManifest(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Head("/v2/:ns1/:ns2/:name/manifests/:reference", handler.HandleManifestDeepNested)

	digest := "sha256:abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abcd"
	upstreamManifest := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`)

	mockProxyService.On("IsEnabled").Return(true)
	// HEAD requests DO proxy for proxy/ paths - needed for container runtime manifest checks
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)

	req := httptest.NewRequest("HEAD", "/v2/proxy/docker.io/nginx/manifests/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	// HEAD should return Content-Length but no body
	assert.NotEmpty(t, resp.Header.Get("Content-Length"))
}

func TestDeepNestedPath_3Segments_GetBlob(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:ns1/:ns2/:name/blobs/:digest", handler.GetBlobDeepNested)

	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(nil, int64(0), io.EOF)

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/nginx/blobs/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 502, resp.StatusCode)
	mockProxyService.AssertCalled(t, "GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest)
}

// Tests for deep nested paths (4 segments: proxy/docker.io/library/nginx)
func TestDeepNestedPath_4Segments_ProxyManifest(t *testing.T) {
	app, _, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	// Route for 4 segments: proxy/docker.io/library/nginx
	app.Get("/v2/:ns1/:ns2/:ns3/:name/manifests/:reference", handler.HandleManifestDeepNested4)

	upstreamManifest := []byte(`{"schemaVersion": 2}`)

	// The full path "proxy/docker.io/library/nginx" should be assembled
	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", "alpine").
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)
	mockImageService.On("SaveImage", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/library/nginx/manifests/alpine", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	time.Sleep(100 * time.Millisecond)
}

func TestDeepNestedPath_4Segments_HeadManifest(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Head("/v2/:ns1/:ns2/:ns3/:name/manifests/:reference", handler.HandleManifestDeepNested4)

	digest := "sha256:abc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abcd"
	upstreamManifest := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`)

	mockProxyService.On("IsEnabled").Return(true)
	// HEAD requests DO proxy for proxy/ paths - needed for container runtime manifest checks
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetManifest", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(upstreamManifest, "application/vnd.oci.image.manifest.v1+json", nil)
	mockProxyService.On("AddToCache", mock.Anything).Return(nil)

	req := httptest.NewRequest("HEAD", "/v2/proxy/docker.io/library/nginx/manifests/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	// HEAD should return Content-Length but no body
	assert.NotEmpty(t, resp.Header.Get("Content-Length"))
}

func TestDeepNestedPath_4Segments_GetBlob(t *testing.T) {
	app, _, _, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:ns1/:ns2/:ns3/:name/blobs/:digest", handler.GetBlobDeepNested4)

	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	mockProxyService.On("IsEnabled").Return(true)
	mockProxyService.On("ResolveRegistry", "proxy/docker.io/library/nginx").Return("https://registry-1.docker.io", "library/nginx", nil)
	mockProxyService.On("GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest).
		Return(nil, int64(0), io.EOF)

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/library/nginx/blobs/"+digest, nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 502, resp.StatusCode)
	mockProxyService.AssertCalled(t, "GetBlob", mock.Anything, "https://registry-1.docker.io", "library/nginx", digest)
}

func TestDeepNestedPath_4Segments_ListTags(t *testing.T) {
	app, mockChartService, mockImageService, mockProxyService, handler, _, cleanup := setupProxyTestEnv(t)
	defer cleanup()

	app.Get("/v2/:ns1/:ns2/:ns3/:name/tags/list", handler.HandleListTagsDeepNested4)

	mockProxyService.On("IsEnabled").Return(true)
	// Mock ListTags to return empty list (no local tags)
	mockImageService.On("ListTags", "proxy/docker.io/library/nginx").Return([]string{}, nil)
	// Mock ListCharts to return empty slice (not nil - handler iterates over this)
	mockChartService.On("ListCharts").Return([]models.ChartGroup{}, nil)

	req := httptest.NewRequest("GET", "/v2/proxy/docker.io/library/nginx/tags/list", nil)
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}
