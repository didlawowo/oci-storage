package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// setupManifestTestEnv creates a test environment for manifest tests
func setupManifestTestEnv(t *testing.T) (*fiber.App, *MockChartService, *MockImageService, *OCIHandler, string, func()) {
	tempDir, err := os.MkdirTemp("", "oci-storage-manifest-test")
	assert.NoError(t, err)

	log := utils.NewLogger(utils.Config{})
	mockChartService := new(MockChartService)
	mockImageService := new(MockImageService)
	pathManager := utils.NewPathManager(tempDir, log)

	mockChartService.On("GetPathManager").Return(pathManager)

	cfg := &config.Config{}
	cfg.Storage.Path = tempDir

	handler := NewOCIHandler(mockChartService, mockImageService, nil, nil, cfg, log)
	app := fiber.New()

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return app, mockChartService, mockImageService, handler, tempDir, cleanup
}

// TestPutManifest_MultiArchIndex tests that multi-arch manifest indexes preserve their exact bytes
// This test reproduces the bug where manifest index digest was corrupted due to JSON re-marshaling
func TestPutManifest_MultiArchIndex(t *testing.T) {
	app, _, mockImageService, handler, tempDir, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)
	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)

	// Create a realistic multi-arch manifest index (like what Docker Buildx produces)
	// Note: The exact formatting matters for digest calculation!
	manifestIndex := `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:fedbda2e286f9b7aae528628e57474dbfdf16d8f6fc79e8a1719231594147c3e",
      "size": 2381,
      "platform": {
        "architecture": "arm64",
        "os": "linux"
      }
    },
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "sha256:814aa2baf5e4476caefc3e7ea0b29f08e4f3c3c98585ad64d44d73b4ac01fe92",
      "size": 2381,
      "platform": {
        "architecture": "amd64",
        "os": "linux"
      }
    }
  ]
}`

	manifestBytes := []byte(manifestIndex)
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBytes))
	expectedSize := len(manifestBytes)

	// Setup mocks
	mockImageService.On("SaveImageIndex", "stock-analyzer", "v1.1.0", manifestBytes, mock.AnythingOfType("int64")).Return(nil)

	// PUT the manifest
	req := httptest.NewRequest("PUT", "/v2/stock-analyzer/manifests/v1.1.0", bytes.NewReader(manifestBytes))
	req.Header.Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 201, resp.StatusCode)

	// Verify Docker-Content-Digest header
	returnedDigest := resp.Header.Get("Docker-Content-Digest")
	assert.Equal(t, expectedDigest, returnedDigest, "Digest in response should match original bytes")

	// Verify manifest was saved to blob storage with correct digest
	// Note: GetBlobPath uses digest as-is, so path is blobs/sha256:xxx not blobs/sha256/xxx
	blobPath := filepath.Join(tempDir, "blobs", expectedDigest)
	assert.FileExists(t, blobPath, "Manifest blob should exist")

	storedData, err := os.ReadFile(blobPath)
	assert.NoError(t, err)
	assert.Equal(t, expectedSize, len(storedData), "Stored manifest size should match original")
	assert.Equal(t, manifestBytes, storedData, "Stored manifest bytes should be identical to original")

	// Verify the stored blob produces the same digest
	storedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(storedData))
	assert.Equal(t, expectedDigest, storedDigest, "Stored blob digest should match expected")

	// Now GET the manifest by tag and verify it returns the exact bytes
	reqGet := httptest.NewRequest("GET", "/v2/stock-analyzer/manifests/v1.1.0", nil)
	reqGet.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")
	respGet, err := app.Test(reqGet)

	assert.NoError(t, err)
	assert.Equal(t, 200, respGet.StatusCode)

	body, err := io.ReadAll(respGet.Body)
	assert.NoError(t, err)
	assert.Equal(t, manifestBytes, body, "GET should return exact original bytes")

	// Also verify GET by digest works
	reqGetDigest := httptest.NewRequest("GET", "/v2/stock-analyzer/manifests/"+expectedDigest, nil)
	reqGetDigest.Header.Set("Accept", "application/vnd.oci.image.index.v1+json")
	respGetDigest, err := app.Test(reqGetDigest)

	assert.NoError(t, err)
	assert.Equal(t, 200, respGetDigest.StatusCode)

	bodyDigest, err := io.ReadAll(respGetDigest.Body)
	assert.NoError(t, err)
	assert.Equal(t, manifestBytes, bodyDigest, "GET by digest should return exact original bytes")
}

// TestPutManifest_SinglePlatform tests that single-platform manifests also preserve exact bytes
func TestPutManifest_SinglePlatform(t *testing.T) {
	app, _, mockImageService, handler, tempDir, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)

	// Single platform manifest (Docker image)
	manifest := `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:abc123",
    "size": 1234
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:layer1",
      "size": 5678
    }
  ]
}`

	manifestBytes := []byte(manifest)
	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestBytes))

	// Setup mocks
	mockImageService.On("SaveImage", "myimage", "latest", &models.OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config: models.OCIDescriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Digest:    "sha256:abc123",
			Size:      1234,
		},
		Layers: []models.OCIDescriptor{
			{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    "sha256:layer1",
				Size:      5678,
			},
		},
	}).Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myimage/manifests/latest", bytes.NewReader(manifestBytes))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 201, resp.StatusCode)
	assert.Equal(t, expectedDigest, resp.Header.Get("Docker-Content-Digest"))

	// Verify blob storage (path is blobs/sha256:xxx not blobs/sha256/xxx)
	blobPath := filepath.Join(tempDir, "blobs", expectedDigest)
	storedData, err := os.ReadFile(blobPath)
	assert.NoError(t, err)
	assert.Equal(t, manifestBytes, storedData, "Stored bytes should match original exactly")
}

// TestPutManifest_DigestVerification verifies that the stored manifest can be retrieved by its declared digest
// This is the core of the bug: if JSON is re-marshaled, the digest changes
func TestPutManifest_DigestVerification(t *testing.T) {
	app, _, mockImageService, handler, _, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)
	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)

	// Use a manifest with specific formatting that would change if re-marshaled
	// (spaces, field order, etc.)
	manifestIndex := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:test123","size":100,"platform":{"architecture":"amd64","os":"linux"}}]}`)

	expectedDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestIndex))

	mockImageService.On("SaveImageIndex", "testimg", "v1", manifestIndex, mock.AnythingOfType("int64")).Return(nil)

	// PUT manifest
	req := httptest.NewRequest("PUT", "/v2/testimg/manifests/v1", bytes.NewReader(manifestIndex))
	resp, err := app.Test(req)
	assert.NoError(t, err)
	assert.Equal(t, 201, resp.StatusCode)

	// Verify we can GET by the declared digest
	reqGet := httptest.NewRequest("GET", "/v2/testimg/manifests/"+expectedDigest, nil)
	respGet, err := app.Test(reqGet)
	assert.NoError(t, err)
	assert.Equal(t, 200, respGet.StatusCode, "Should be able to retrieve manifest by its correct digest")

	body, _ := io.ReadAll(respGet.Body)
	actualDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	assert.Equal(t, expectedDigest, actualDigest, "Retrieved content digest must match requested digest")
}

// TestManifestDigestIntegrity simulates what happens during a multi-arch image push
// and verifies that the declared sizes/digests in the index match what's actually stored
func TestManifestDigestIntegrity(t *testing.T) {
	app, _, mockImageService, handler, _, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)
	app.Get("/v2/:name/manifests/:reference", handler.HandleManifest)
	app.Head("/v2/:name/blobs/:digest", handler.HeadBlob)

	// Step 1: Push platform-specific manifests (arm64 and amd64)
	arm64Manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:arm64config","size":500},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:arm64layer","size":10000}]}`)
	amd64Manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:amd64config","size":500},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:amd64layer","size":10000}]}`)

	arm64Digest := fmt.Sprintf("sha256:%x", sha256.Sum256(arm64Manifest))
	amd64Digest := fmt.Sprintf("sha256:%x", sha256.Sum256(amd64Manifest))

	mockImageService.On("SaveImage", "multiarch-test", arm64Digest, &models.OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        models.OCIDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:arm64config", Size: 500},
		Layers:        []models.OCIDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:arm64layer", Size: 10000}},
	}).Return(nil)

	mockImageService.On("SaveImage", "multiarch-test", amd64Digest, &models.OCIManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.oci.image.manifest.v1+json",
		Config:        models.OCIDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: "sha256:amd64config", Size: 500},
		Layers:        []models.OCIDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: "sha256:amd64layer", Size: 10000}},
	}).Return(nil)

	// Push arm64 manifest by digest
	req1 := httptest.NewRequest("PUT", "/v2/multiarch-test/manifests/"+arm64Digest, bytes.NewReader(arm64Manifest))
	resp1, _ := app.Test(req1)
	assert.Equal(t, 201, resp1.StatusCode)

	// Push amd64 manifest by digest
	req2 := httptest.NewRequest("PUT", "/v2/multiarch-test/manifests/"+amd64Digest, bytes.NewReader(amd64Manifest))
	resp2, _ := app.Test(req2)
	assert.Equal(t, 201, resp2.StatusCode)

	// Step 2: Create and push the manifest index referencing the platform manifests
	// CRITICAL: The sizes declared here MUST match actual stored blob sizes
	manifestIndex := fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "%s",
      "size": %d,
      "platform": {"architecture": "arm64", "os": "linux"}
    },
    {
      "mediaType": "application/vnd.oci.image.manifest.v1+json",
      "digest": "%s",
      "size": %d,
      "platform": {"architecture": "amd64", "os": "linux"}
    }
  ]
}`, arm64Digest, len(arm64Manifest), amd64Digest, len(amd64Manifest))

	indexBytes := []byte(manifestIndex)
	indexDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(indexBytes))

	mockImageService.On("SaveImageIndex", "multiarch-test", "latest", indexBytes, mock.AnythingOfType("int64")).Return(nil)

	req3 := httptest.NewRequest("PUT", "/v2/multiarch-test/manifests/latest", bytes.NewReader(indexBytes))
	resp3, _ := app.Test(req3)
	assert.Equal(t, 201, resp3.StatusCode)
	assert.Equal(t, indexDigest, resp3.Header.Get("Docker-Content-Digest"))

	// Step 3: Verify we can retrieve the manifest index by tag
	reqGet := httptest.NewRequest("GET", "/v2/multiarch-test/manifests/latest", nil)
	reqGet.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, */*")
	respGet, _ := app.Test(reqGet)
	assert.Equal(t, 200, respGet.StatusCode)

	bodyGet, _ := io.ReadAll(respGet.Body)
	assert.Equal(t, indexBytes, bodyGet, "Index bytes must be preserved exactly")

	// Step 4: Parse the returned index and verify the child manifests are retrievable
	var returnedIndex models.OCIIndex
	err := json.Unmarshal(bodyGet, &returnedIndex)
	assert.NoError(t, err)

	for _, desc := range returnedIndex.Manifests {
		// Verify we can retrieve each platform manifest by its digest
		reqChild := httptest.NewRequest("GET", "/v2/multiarch-test/manifests/"+desc.Digest, nil)
		respChild, _ := app.Test(reqChild)
		assert.Equal(t, 200, respChild.StatusCode, "Should retrieve child manifest %s", desc.Digest)

		childBody, _ := io.ReadAll(respChild.Body)

		// CRITICAL: The actual size must match declared size in index
		assert.Equal(t, int(desc.Size), len(childBody),
			"Child manifest size mismatch for %s: declared %d, actual %d",
			desc.Digest, desc.Size, len(childBody))

		// CRITICAL: The actual digest must match declared digest
		actualChildDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(childBody))
		assert.Equal(t, desc.Digest, actualChildDigest,
			"Child manifest digest mismatch: declared %s, actual %s",
			desc.Digest, actualChildDigest)
	}
}

// TestPutManifest_RejectsSizeMismatch verifies that PutManifest rejects a manifest
// when its declared layer sizes don't match the actual blobs on disk.
// This prevents the "failed size validation: X != Y" error during pull.
func TestPutManifest_RejectsSizeMismatch(t *testing.T) {
	app, _, mockImageService, handler, tempDir, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)

	// Step 1: Create a real blob on disk (simulating a prior push)
	blobContent := []byte("this is a fake layer blob with some content for testing")
	blobDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(blobContent))
	blobPath := filepath.Join(tempDir, "blobs", blobDigest)
	err := os.MkdirAll(filepath.Dir(blobPath), 0755)
	assert.NoError(t, err)
	err = os.WriteFile(blobPath, blobContent, 0644)
	assert.NoError(t, err)

	actualSize := int64(len(blobContent))
	wrongSize := actualSize + 999 // deliberately wrong

	// Step 2: Create a manifest that declares wrong size for the layer
	manifest := fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:configdigest000",
    "size": 0
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "%s",
      "size": %d
    }
  ]
}`, blobDigest, wrongSize)

	// Step 3: Push manifest - should be rejected with 400
	req := httptest.NewRequest("PUT", "/v2/testimage/manifests/v1.0.0", bytes.NewReader([]byte(manifest)))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode, "Manifest with wrong layer size should be rejected")

	// Verify the error message mentions size mismatch
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "BLOB_SIZE_MISMATCH")

	// Step 4: Now push with correct size - should succeed
	_ = mockImageService // will be called on success path
	correctManifest := fmt.Sprintf(`{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:configdigest000",
    "size": 0
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "%s",
      "size": %d
    }
  ]
}`, blobDigest, actualSize)

	mockImageService.On("SaveImage", "testimage", "v1.0.0", mock.AnythingOfType("*models.OCIManifest")).Return(nil)

	req2 := httptest.NewRequest("PUT", "/v2/testimage/manifests/v1.0.0", bytes.NewReader([]byte(correctManifest)))
	req2.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp2, err := app.Test(req2)

	assert.NoError(t, err)
	assert.Equal(t, 201, resp2.StatusCode, "Manifest with correct layer size should be accepted")
}

// TestPutManifest_AllowsMissingBlobs verifies that a manifest referencing blobs
// not yet on disk is still accepted (cross-repo mount scenario)
func TestPutManifest_AllowsMissingBlobs(t *testing.T) {
	app, _, mockImageService, handler, _, cleanup := setupManifestTestEnv(t)
	defer cleanup()

	app.Put("/v2/:name/manifests/:reference", handler.PutManifest)

	// Manifest references a blob that doesn't exist locally
	manifest := `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {
    "mediaType": "application/vnd.oci.image.config.v1+json",
    "digest": "sha256:nonexistentconfig",
    "size": 1234
  },
  "layers": [
    {
      "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
      "digest": "sha256:nonexistentlayer",
      "size": 5678
    }
  ]
}`

	mockImageService.On("SaveImage", "testimage", "latest", mock.AnythingOfType("*models.OCIManifest")).Return(nil)

	req := httptest.NewRequest("PUT", "/v2/testimage/manifests/latest", bytes.NewReader([]byte(manifest)))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	resp, err := app.Test(req)

	assert.NoError(t, err)
	assert.Equal(t, 201, resp.StatusCode, "Manifest with missing blobs should still be accepted")
}
