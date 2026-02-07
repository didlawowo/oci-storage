// pkg/services/proxy.go
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// ProxyService handles Docker registry proxying and caching
type ProxyService struct {
	config      *config.Config
	pathManager *utils.PathManager
	log         *utils.Logger
	httpClient  *http.Client
	cacheMutex  sync.RWMutex
	cacheState  *models.CacheState
}

// NewProxyService creates a new proxy service
func NewProxyService(cfg *config.Config, log *utils.Logger) *ProxyService {
	pm := utils.NewPathManager(cfg.Storage.Path, log)

	svc := &ProxyService{
		config:      cfg,
		pathManager: pm,
		log:         log,
		httpClient: &http.Client{
			// No global timeout - we use context timeouts per-request instead
			// Global timeout would kill large blob downloads (5GB+ can take 10+ minutes)
			Timeout: 0,
		},
		cacheState: &models.CacheState{
			MaxSize: int64(cfg.Proxy.Cache.MaxSizeGB) * 1024 * 1024 * 1024,
		},
	}

	// Load existing cache state
	svc.loadCacheState()

	log.WithFields(logrus.Fields{
		"enabled":    cfg.Proxy.Enabled,
		"maxSizeGB":  cfg.Proxy.Cache.MaxSizeGB,
		"registries": len(cfg.Proxy.Registries),
	}).Info("Proxy service initialized")

	return svc
}

// IsEnabled returns whether the proxy is enabled
func (s *ProxyService) IsEnabled() bool {
	return s.config.Proxy.Enabled
}

// ResolveRegistry parses an image path and determines the upstream registry
// Supports formats:
//   - proxy/docker.io/nginx -> docker.io registry, library/nginx image
//   - proxy/docker.io/library/nginx -> docker.io registry, library/nginx image
//   - proxy/ghcr.io/org/image -> ghcr.io registry, org/image
//   - docker.io/nginx -> docker.io registry, library/nginx image
//   - nginx -> default registry, library/nginx image
func (s *ProxyService) ResolveRegistry(imagePath string) (registryURL string, imageName string, err error) {
	// Strip "proxy/" prefix if present
	imagePath = strings.TrimPrefix(imagePath, "proxy/")

	parts := strings.SplitN(imagePath, "/", 2)

	// Check if first part matches a configured registry
	for _, reg := range s.config.Proxy.Registries {
		if parts[0] == reg.Name {
			if len(parts) > 1 {
				finalImageName := parts[1]
				// For Docker Hub, add library/ prefix for single-segment image names
				if reg.Name == "docker.io" && !strings.Contains(finalImageName, "/") {
					finalImageName = "library/" + finalImageName
				}
				return reg.URL, finalImageName, nil
			}
			return "", "", fmt.Errorf("invalid image path: %s", imagePath)
		}
	}

	// No registry prefix found - use default
	defaultReg := s.GetDefaultRegistry()

	// Handle Docker Hub's "library/" prefix for official images
	if !strings.Contains(imagePath, "/") {
		imagePath = "library/" + imagePath
	}

	return defaultReg, imagePath, nil
}

// GetDefaultRegistry returns the default upstream registry URL
func (s *ProxyService) GetDefaultRegistry() string {
	for _, reg := range s.config.Proxy.Registries {
		if reg.Default {
			return reg.URL
		}
	}
	// Fallback to Docker Hub
	return "https://registry-1.docker.io"
}

// GetManifest fetches a manifest from upstream registry
func (s *ProxyService) GetManifest(ctx context.Context, registryURL, name, reference string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", registryURL, name, reference)

	s.log.WithFields(logrus.Fields{
		"registry":  registryURL,
		"name":      name,
		"reference": reference,
		"url":       url,
	}).Debug("Fetching manifest from upstream")

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	// Accept multiple manifest types
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))

	resp, err := s.fetchWithAuth(ctx, req, registryURL, name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("upstream returned status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	s.log.WithFields(logrus.Fields{
		"contentType": contentType,
		"size":        len(data),
	}).Debug("Manifest fetched successfully")

	return data, contentType, nil
}

// GetBlob fetches a blob from upstream registry
func (s *ProxyService) GetBlob(ctx context.Context, registryURL, name, digest string) (io.ReadCloser, int64, error) {
	url := fmt.Sprintf("%s/v2/%s/blobs/%s", registryURL, name, digest)

	s.log.WithFields(logrus.Fields{
		"registry": registryURL,
		"name":     name,
		"digest":   digest,
	}).Debug("Fetching blob from upstream")

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.fetchWithAuth(ctx, req, registryURL, name)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch blob: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

// fetchWithAuth handles Docker registry authentication flow
func (s *ProxyService) fetchWithAuth(ctx context.Context, req *http.Request, registryURL, name string) (*http.Response, error) {
	s.log.WithFields(logrus.Fields{
		"url":    req.URL.String(),
		"method": req.Method,
	}).Debug("Making upstream request")

	// Find registry config to get credentials
	var regConfig *config.RegistryConfig
	for i := range s.config.Proxy.Registries {
		if s.config.Proxy.Registries[i].URL == registryURL {
			regConfig = &s.config.Proxy.Registries[i]
			break
		}
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.log.WithError(err).Error("Upstream request failed")
		return nil, err
	}

	s.log.WithField("status", resp.StatusCode).Debug("Upstream response received")

	// Handle 401 - need to get token
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		s.log.Debug("Got 401, fetching auth token...")

		wwwAuth := resp.Header.Get("Www-Authenticate")
		token, err := s.getToken(ctx, wwwAuth, name, regConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to get auth token: %w", err)
		}

		s.log.Debug("Token obtained, retrying request with auth")
		// Retry with token
		req.Header.Set("Authorization", "Bearer "+token)
		return s.httpClient.Do(req)
	}

	return resp, nil
}

// getToken parses WWW-Authenticate and fetches token (with optional credentials)
func (s *ProxyService) getToken(ctx context.Context, wwwAuth, name string, regConfig *config.RegistryConfig) (string, error) {
	// Parse: Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/alpine:pull"
	params := s.parseWwwAuthenticate(wwwAuth)

	realm := params["realm"]
	service := params["service"]
	scope := params["scope"]

	// If scope is not provided in header, construct it
	if scope == "" {
		scope = fmt.Sprintf("repository:%s:pull", name)
	}

	tokenURL := fmt.Sprintf("%s?service=%s&scope=%s", realm, service, scope)

	s.log.WithFields(logrus.Fields{
		"tokenURL":       tokenURL,
		"hasCredentials": regConfig != nil && regConfig.Username != "",
	}).Debug("Fetching auth token")

	req, err := http.NewRequestWithContext(ctx, "GET", tokenURL, nil)
	if err != nil {
		return "", err
	}

	// Add Basic Auth if credentials are configured for this registry
	if regConfig != nil && regConfig.Username != "" && regConfig.Password != "" {
		req.SetBasicAuth(regConfig.Username, regConfig.Password)
		s.log.WithField("username", regConfig.Username).Debug("Using credentials for token request")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}

	// Some registries use "token", others use "access_token"
	if tokenResp.Token != "" {
		return tokenResp.Token, nil
	}
	return tokenResp.AccessToken, nil
}

// parseWwwAuthenticate parses WWW-Authenticate header
func (s *ProxyService) parseWwwAuthenticate(header string) map[string]string {
	params := make(map[string]string)

	// Remove "Bearer " prefix
	header = strings.TrimPrefix(header, "Bearer ")

	// Parse key="value" pairs
	re := regexp.MustCompile(`(\w+)="([^"]*)"`)
	matches := re.FindAllStringSubmatch(header, -1)

	for _, match := range matches {
		if len(match) == 3 {
			params[match[1]] = match[2]
		}
	}

	return params
}

// GetCacheState returns the current cache state calculated from filesystem
func (s *ProxyService) GetCacheState() *models.CacheState {
	images, err := s.GetCachedImages()
	if err != nil {
		s.log.WithError(err).Warn("Failed to get cached images for state")
		return &models.CacheState{
			MaxSize: int64(s.config.Proxy.Cache.MaxSizeGB) * 1024 * 1024 * 1024,
		}
	}

	var totalSize int64
	for _, img := range images {
		totalSize += img.Size
	}

	state := &models.CacheState{
		TotalSize: totalSize,
		MaxSize:   int64(s.config.Proxy.Cache.MaxSizeGB) * 1024 * 1024 * 1024,
		ItemCount: len(images),
	}
	state.CalculateUsagePercent()

	return state
}

// GetCachedImages returns all cached images metadata by scanning the filesystem
// This ensures consistency - the filesystem is the single source of truth
func (s *ProxyService) GetCachedImages() ([]models.CachedImageMetadata, error) {
	metadataDir := filepath.Join(s.pathManager.GetBasePath(), "cache", "metadata")

	// Ensure directory exists
	if _, err := os.Stat(metadataDir); os.IsNotExist(err) {
		return []models.CachedImageMetadata{}, nil
	}

	files, err := os.ReadDir(metadataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache metadata directory: %w", err)
	}

	var images []models.CachedImageMetadata
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(metadataDir, file.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name()).Warn("Failed to read cache metadata file")
			continue
		}

		var metadata models.CachedImageMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			s.log.WithError(err).WithField("file", file.Name()).Warn("Failed to parse cache metadata file")
			continue
		}

		// Skip entries with invalid/corrupted tags (digest fragments, etc)
		// Valid tags should not contain ":" (digest fragments like "56:1" or "sha256:...")
		if strings.Contains(metadata.Tag, ":") || len(metadata.Tag) < 2 {
			s.log.WithFields(logrus.Fields{
				"file": file.Name(),
				"tag":  metadata.Tag,
			}).Debug("Skipping corrupted cache entry")
			continue
		}

		images = append(images, metadata)
	}

	return images, nil
}

// UpdateAccessTime updates the last accessed time for a cached image
func (s *ProxyService) UpdateAccessTime(name, tag string) {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	for i := range s.cacheState.Images {
		if s.cacheState.Images[i].Name == name && s.cacheState.Images[i].Tag == tag {
			s.cacheState.Images[i].LastAccessed = time.Now()
			s.cacheState.Images[i].AccessCount++
			break
		}
	}

	// Persist asynchronously
	go s.saveCacheState()
}

// AddToCache adds image metadata to the cache tracking
// Each image is stored as an individual file for consistency
func (s *ProxyService) AddToCache(metadata models.CachedImageMetadata) error {
	// Skip invalid or digest-like references - only cache actual tags
	// Valid tags: latest, v1.0, alpine, 3.12-alpine, etc.
	// Invalid: sha256:..., sha2, :abc, manifest fragments, etc.
	tag := metadata.Tag
	if strings.HasPrefix(tag, "sha") ||
		strings.HasPrefix(tag, ":") ||
		strings.Contains(tag, "manifest") ||
		len(tag) < 2 ||
		len(tag) > 128 {
		s.log.WithFields(logrus.Fields{
			"name": metadata.Name,
			"tag":  tag,
		}).Debug("Skipping cache for invalid/digest reference")
		return nil
	}

	metadataPath := s.pathManager.GetCachedImageMetadataPath(metadata.Name, metadata.Tag)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		return fmt.Errorf("failed to create cache metadata directory: %w", err)
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache metadata: %w", err)
	}

	if err := os.WriteFile(metadataPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache metadata: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name": metadata.Name,
		"tag":  metadata.Tag,
		"path": metadataPath,
	}).Debug("Cache metadata saved")

	// Check if we need to evict (recalculate total size)
	s.checkAndEvictIfNeeded()

	return nil
}

// checkAndEvictIfNeeded calculates total cache size and evicts if over limit
func (s *ProxyService) checkAndEvictIfNeeded() {
	images, err := s.GetCachedImages()
	if err != nil {
		s.log.WithError(err).Warn("Failed to get cached images for eviction check")
		return
	}

	var totalSize int64
	for _, img := range images {
		totalSize += img.Size
	}

	maxSize := int64(s.config.Proxy.Cache.MaxSizeGB) * 1024 * 1024 * 1024
	if totalSize > maxSize {
		s.log.WithFields(logrus.Fields{
			"totalSize": totalSize,
			"maxSize":   maxSize,
		}).Info("Cache over limit, triggering eviction")

		// Sort by last accessed and evict oldest
		sort.Slice(images, func(i, j int) bool {
			return images[i].LastAccessed.Before(images[j].LastAccessed)
		})

		targetSize := maxSize * 90 / 100 // Evict to 90% capacity
		for _, img := range images {
			if totalSize <= targetSize {
				break
			}

			s.log.WithFields(logrus.Fields{
				"image": img.Name,
				"tag":   img.Tag,
				"size":  img.Size,
			}).Info("Evicting cached image (LRU)")

			if err := s.deleteCachedImageFiles(img.Name, img.Tag); err != nil {
				s.log.WithError(err).Warn("Failed to delete cached image files during eviction")
			}

			// Delete metadata file
			metadataPath := s.pathManager.GetCachedImageMetadataPath(img.Name, img.Tag)
			os.Remove(metadataPath)

			totalSize -= img.Size
		}
	}
}

// EvictLRU removes least recently used images until target size is reached
func (s *ProxyService) EvictLRU(targetBytes int64) error {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	return s.evictLRUInternal(targetBytes)
}

// evictLRUInternal performs LRU eviction (caller must hold lock)
func (s *ProxyService) evictLRUInternal(targetBytes int64) error {
	if s.cacheState.TotalSize <= targetBytes {
		return nil // Already under limit
	}

	// Sort by last accessed (oldest first)
	sort.Slice(s.cacheState.Images, func(i, j int) bool {
		return s.cacheState.Images[i].LastAccessed.Before(s.cacheState.Images[j].LastAccessed)
	})

	// Evict until under limit
	for s.cacheState.TotalSize > targetBytes && len(s.cacheState.Images) > 0 {
		oldest := s.cacheState.Images[0]

		s.log.WithFields(logrus.Fields{
			"image":        oldest.Name,
			"tag":          oldest.Tag,
			"lastAccessed": oldest.LastAccessed,
			"size":         oldest.Size,
		}).Info("Evicting cached image (LRU)")

		// Delete files
		if err := s.deleteCachedImageFiles(oldest.Name, oldest.Tag); err != nil {
			s.log.WithError(err).Warn("Failed to delete cached image files")
		}

		// Update state
		s.cacheState.TotalSize -= oldest.Size
		s.cacheState.Images = s.cacheState.Images[1:]
		s.cacheState.ItemCount--
	}

	return s.saveCacheState()
}

// DeleteCachedImage removes a specific cached image
func (s *ProxyService) DeleteCachedImage(name, tag string) error {
	// Delete the cache metadata file (source of truth)
	metadataPath := s.pathManager.GetCachedImageMetadataPath(name, tag)
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).WithField("path", metadataPath).Warn("Failed to delete cache metadata file")
	}

	// Delete image files (manifests, tags)
	s.deleteCachedImageFiles(name, tag)

	s.log.WithField("name", name).WithField("tag", tag).Info("Cached image deleted")
	return nil
}

// deleteCachedImageFiles removes the files for a cached image
func (s *ProxyService) deleteCachedImageFiles(name, tag string) error {
	basePath := s.pathManager.GetBasePath()

	// Delete cache metadata
	metadataPath := s.pathManager.GetCachedImageMetadataPath(name, tag)
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Warn("Failed to delete cache metadata file")
	}

	// Delete image manifest (in manifests/ directory)
	manifestPath := filepath.Join(basePath, "images", name, "manifests", tag+".json")
	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Debug("Failed to delete manifest file")
	} else {
		s.log.WithField("path", manifestPath).Debug("Deleted manifest file")
	}

	// Delete image tag metadata (in tags/ directory)
	tagMetadataPath := filepath.Join(basePath, "images", name, "tags", tag+".json")
	if err := os.Remove(tagMetadataPath); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Debug("Failed to delete tag metadata file")
	} else {
		s.log.WithField("path", tagMetadataPath).Debug("Deleted tag metadata file")
	}

	// Clean up empty directories (tags, manifests, then image dir)
	tagsDir := filepath.Join(basePath, "images", name, "tags")
	manifestsDir := filepath.Join(basePath, "images", name, "manifests")
	imageDir := filepath.Join(basePath, "images", name)

	s.cleanEmptyDirs(tagsDir)
	s.cleanEmptyDirs(manifestsDir)
	s.cleanEmptyDirs(imageDir)

	// Note: We don't delete blobs as they might be shared with other images
	// A garbage collection process could be added later

	return nil
}

// cleanEmptyDirs removes empty directories recursively up to images/
func (s *ProxyService) cleanEmptyDirs(dir string) {
	imagesBase := s.pathManager.GetBasePath() + "/images"

	for dir != imagesBase && dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
}

// PurgeAllCache removes all cached images and blobs completely
func (s *ProxyService) PurgeAllCache() error {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	s.log.Info("Purging all cache data")

	// Delete all blobs
	blobsDir := s.pathManager.GetBasePath() + "/blobs"
	if err := os.RemoveAll(blobsDir); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Warn("Failed to delete blobs directory")
	}
	// Recreate empty blobs directory
	if err := os.MkdirAll(blobsDir, 0755); err != nil {
		s.log.WithError(err).Warn("Failed to recreate blobs directory")
	}

	// Delete all images directory (manifests and tags metadata)
	imagesDir := s.pathManager.GetBasePath() + "/images"
	if err := os.RemoveAll(imagesDir); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Warn("Failed to delete images directory")
	}
	// Recreate empty images directory
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		s.log.WithError(err).Warn("Failed to recreate images directory")
	}

	// Delete all cache metadata files
	cacheMetadataDir := s.pathManager.GetBasePath() + "/cache/metadata"
	if err := os.RemoveAll(cacheMetadataDir); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Warn("Failed to delete cache metadata directory")
	}
	// Recreate empty cache metadata directory
	if err := os.MkdirAll(cacheMetadataDir, 0755); err != nil {
		s.log.WithError(err).Warn("Failed to recreate cache metadata directory")
	}

	// Reset cache state in memory
	s.cacheState.Images = []models.CachedImageMetadata{}
	s.cacheState.TotalSize = 0
	s.cacheState.ItemCount = 0

	// Delete legacy state.json if exists (we use filesystem as source of truth now)
	statePath := s.pathManager.GetCacheStatePath()
	os.Remove(statePath)

	s.log.Info("Cache purged successfully")
	return nil
}

// loadCacheState loads the cache state from disk
func (s *ProxyService) loadCacheState() {
	statePath := s.pathManager.GetCacheStatePath()

	data, err := os.ReadFile(statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.WithError(err).Warn("Failed to load cache state")
		}
		return
	}

	var state models.CacheState
	if err := json.Unmarshal(data, &state); err != nil {
		s.log.WithError(err).Warn("Failed to parse cache state")
		return
	}

	s.cacheState = &state
	s.cacheState.MaxSize = int64(s.config.Proxy.Cache.MaxSizeGB) * 1024 * 1024 * 1024

	s.log.WithFields(logrus.Fields{
		"totalSize": s.cacheState.TotalSize,
		"itemCount": s.cacheState.ItemCount,
	}).Info("Cache state loaded")
}

// saveCacheState persists the cache state to disk
func (s *ProxyService) saveCacheState() error {
	statePath := s.pathManager.GetCacheStatePath()

	data, err := json.MarshalIndent(s.cacheState, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache state: %w", err)
	}

	if err := os.WriteFile(statePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache state: %w", err)
	}

	return nil
}
