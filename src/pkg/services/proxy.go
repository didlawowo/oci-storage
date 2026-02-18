// pkg/services/proxy.go
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/storage"
	"oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// ProxyService handles Docker registry proxying and caching
type ProxyService struct {
	config      *config.Config
	pathManager *utils.PathManager
	backend     storage.Backend
	log         *utils.Logger
	httpClient  *http.Client
	cacheMutex  sync.RWMutex
	cacheState  *models.CacheState
}

// NewProxyService creates a new proxy service
func NewProxyService(cfg *config.Config, log *utils.Logger, pm *utils.PathManager, backend storage.Backend) *ProxyService {
	// Configure HTTP transport with connection pooling to prevent fd exhaustion
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		MaxConnsPerHost:     50,
		IdleConnTimeout:     90 * time.Second,
	}

	svc := &ProxyService{
		config:      cfg,
		pathManager: pm,
		backend:     backend,
		log:         log,
		httpClient: &http.Client{
			Timeout:   0,
			Transport: transport,
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
func (s *ProxyService) ResolveRegistry(imagePath string) (registryURL string, imageName string, err error) {
	imagePath = strings.TrimPrefix(imagePath, "proxy/")

	parts := strings.SplitN(imagePath, "/", 2)

	for _, reg := range s.config.Proxy.Registries {
		if parts[0] == reg.Name {
			if len(parts) > 1 {
				finalImageName := parts[1]
				if reg.Name == "docker.io" && !strings.Contains(finalImageName, "/") {
					finalImageName = "library/" + finalImageName
				}
				return reg.URL, finalImageName, nil
			}
			return "", "", fmt.Errorf("invalid image path: %s", imagePath)
		}
	}

	defaultReg := s.GetDefaultRegistry()

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

	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.oci.image.index.v1+json",
	}, ", "))

	resp, err := s.FetchWithAuth(ctx, req, registryURL, name)
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

	resp, err := s.FetchWithAuth(ctx, req, registryURL, name)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to fetch blob: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, nil
}

// FetchWithAuth handles Docker registry authentication flow
func (s *ProxyService) FetchWithAuth(ctx context.Context, req *http.Request, registryURL, name string) (*http.Response, error) {
	s.log.WithFields(logrus.Fields{
		"url":    req.URL.String(),
		"method": req.Method,
	}).Debug("Making upstream request")

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

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		s.log.Debug("Got 401, fetching auth token...")

		wwwAuth := resp.Header.Get("Www-Authenticate")
		token, err := s.getToken(ctx, wwwAuth, name, regConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to get auth token: %w", err)
		}

		s.log.Debug("Token obtained, retrying request with auth")
		req.Header.Set("Authorization", "Bearer "+token)
		return s.httpClient.Do(req)
	}

	return resp, nil
}

func (s *ProxyService) getToken(ctx context.Context, wwwAuth, name string, regConfig *config.RegistryConfig) (string, error) {
	params := s.parseWwwAuthenticate(wwwAuth)

	realm := params["realm"]
	service := params["service"]
	scope := params["scope"]

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

	if tokenResp.Token != "" {
		return tokenResp.Token, nil
	}
	return tokenResp.AccessToken, nil
}

func (s *ProxyService) parseWwwAuthenticate(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimPrefix(header, "Bearer ")

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

// GetCachedImages returns all cached images metadata by scanning the storage
func (s *ProxyService) GetCachedImages() ([]models.CachedImageMetadata, error) {
	metadataDir := filepath.Join("cache", "metadata")

	exists, _ := s.backend.Exists(metadataDir)
	if !exists {
		return []models.CachedImageMetadata{}, nil
	}

	files, err := s.backend.List(metadataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache metadata directory: %w", err)
	}

	var images []models.CachedImageMetadata
	for _, file := range files {
		if file.IsDir || !strings.HasSuffix(file.Name, ".json") {
			continue
		}

		filePath := filepath.Join(metadataDir, file.Name)
		data, err := s.backend.Read(filePath)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name).Warn("Failed to read cache metadata file")
			continue
		}

		var metadata models.CachedImageMetadata
		if err := json.Unmarshal(data, &metadata); err != nil {
			s.log.WithError(err).WithField("file", file.Name).Warn("Failed to parse cache metadata file")
			continue
		}

		if strings.Contains(metadata.Tag, ":") || len(metadata.Tag) < 2 {
			s.log.WithFields(logrus.Fields{
				"file": file.Name,
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

	go s.saveCacheState()
}

// AddToCache adds image metadata to the cache tracking
func (s *ProxyService) AddToCache(metadata models.CachedImageMetadata) error {
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

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache metadata: %w", err)
	}

	if err := s.backend.Write(metadataPath, data); err != nil {
		return fmt.Errorf("failed to write cache metadata: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name": metadata.Name,
		"tag":  metadata.Tag,
		"path": metadataPath,
	}).Debug("Cache metadata saved")

	s.checkAndEvictIfNeeded()

	return nil
}

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

		sort.Slice(images, func(i, j int) bool {
			return images[i].LastAccessed.Before(images[j].LastAccessed)
		})

		targetSize := maxSize * 90 / 100
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

			metadataPath := s.pathManager.GetCachedImageMetadataPath(img.Name, img.Tag)
			s.backend.Delete(metadataPath)

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

func (s *ProxyService) evictLRUInternal(targetBytes int64) error {
	if s.cacheState.TotalSize <= targetBytes {
		return nil
	}

	sort.Slice(s.cacheState.Images, func(i, j int) bool {
		return s.cacheState.Images[i].LastAccessed.Before(s.cacheState.Images[j].LastAccessed)
	})

	for s.cacheState.TotalSize > targetBytes && len(s.cacheState.Images) > 0 {
		oldest := s.cacheState.Images[0]

		s.log.WithFields(logrus.Fields{
			"image":        oldest.Name,
			"tag":          oldest.Tag,
			"lastAccessed": oldest.LastAccessed,
			"size":         oldest.Size,
		}).Info("Evicting cached image (LRU)")

		if err := s.deleteCachedImageFiles(oldest.Name, oldest.Tag); err != nil {
			s.log.WithError(err).Warn("Failed to delete cached image files")
		}

		s.cacheState.TotalSize -= oldest.Size
		s.cacheState.Images = s.cacheState.Images[1:]
		s.cacheState.ItemCount--
	}

	return s.saveCacheState()
}

// DeleteCachedImage removes a specific cached image
func (s *ProxyService) DeleteCachedImage(name, tag string) error {
	metadataPath := s.pathManager.GetCachedImageMetadataPath(name, tag)
	if err := s.backend.Delete(metadataPath); err != nil {
		s.log.WithError(err).WithField("path", metadataPath).Warn("Failed to delete cache metadata file")
	}

	s.deleteCachedImageFiles(name, tag)

	s.log.WithField("name", name).WithField("tag", tag).Info("Cached image deleted")
	return nil
}

// deleteCachedImageFiles removes the files for a cached image
func (s *ProxyService) deleteCachedImageFiles(name, tag string) error {
	// Delete cache metadata
	metadataPath := s.pathManager.GetCachedImageMetadataPath(name, tag)
	if err := s.backend.Delete(metadataPath); err != nil {
		s.log.WithError(err).Warn("Failed to delete cache metadata file")
	}

	// Delete image manifest
	manifestPath := filepath.Join("images", name, "manifests", tag+".json")
	if err := s.backend.Delete(manifestPath); err != nil {
		s.log.WithError(err).Debug("Failed to delete manifest file")
	} else {
		s.log.WithField("path", manifestPath).Debug("Deleted manifest file")
	}

	// Delete image tag metadata
	tagMetadataPath := filepath.Join("images", name, "tags", tag+".json")
	if err := s.backend.Delete(tagMetadataPath); err != nil {
		s.log.WithError(err).Debug("Failed to delete tag metadata file")
	} else {
		s.log.WithField("path", tagMetadataPath).Debug("Deleted tag metadata file")
	}

	return nil
}

// PurgeAllCache removes all cached images and blobs completely
func (s *ProxyService) PurgeAllCache() error {
	s.cacheMutex.Lock()
	defer s.cacheMutex.Unlock()

	s.log.Info("Purging all cache data")

	// Delete all blobs
	if err := s.backend.RemoveAll("blobs"); err != nil {
		s.log.WithError(err).Warn("Failed to delete blobs directory")
	}

	// Delete all images directory
	if err := s.backend.RemoveAll("images"); err != nil {
		s.log.WithError(err).Warn("Failed to delete images directory")
	}

	// Delete all cache metadata files
	if err := s.backend.RemoveAll(filepath.Join("cache", "metadata")); err != nil {
		s.log.WithError(err).Warn("Failed to delete cache metadata directory")
	}

	// Reset cache state in memory
	s.cacheState.Images = []models.CachedImageMetadata{}
	s.cacheState.TotalSize = 0
	s.cacheState.ItemCount = 0

	// Delete legacy state.json
	statePath := s.pathManager.GetCacheStatePath()
	s.backend.Delete(statePath)

	s.log.Info("Cache purged successfully")
	return nil
}

// loadCacheState loads the cache state from storage
func (s *ProxyService) loadCacheState() {
	statePath := s.pathManager.GetCacheStatePath()

	data, err := s.backend.Read(statePath)
	if err != nil {
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

// saveCacheState persists the cache state to storage
func (s *ProxyService) saveCacheState() error {
	statePath := s.pathManager.GetCacheStatePath()

	data, err := json.MarshalIndent(s.cacheState, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache state: %w", err)
	}

	if err := s.backend.Write(statePath, data); err != nil {
		return fmt.Errorf("failed to write cache state: %w", err)
	}

	return nil
}
