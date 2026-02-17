// pkg/services/image.go
package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/storage"
	utils "oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// ImageService handles Docker image operations
type ImageService struct {
	pathManager *utils.PathManager
	backend     storage.Backend
	config      *config.Config
	log         *utils.Logger
}

// NewImageService creates a new image service
func NewImageService(config *config.Config, log *utils.Logger, pm *utils.PathManager, backend storage.Backend) *ImageService {
	return &ImageService{
		pathManager: pm,
		backend:     backend,
		config:      config,
		log:         log,
	}
}

// GetPathManager returns the path manager
func (s *ImageService) GetPathManager() *utils.PathManager {
	return s.pathManager
}

// SaveImage saves Docker image metadata (manifest should already be saved by handler)
// IMPORTANT: This function only saves metadata, NOT the manifest itself.
// The manifest must be saved separately using raw bytes to preserve digest integrity.
func (s *ImageService) SaveImage(name, reference string, manifest *models.OCIManifest) error {
	// Skip saving metadata for digest references - only save for actual tags
	if strings.HasPrefix(reference, "sha256:") {
		s.log.WithFields(logrus.Fields{
			"name":      name,
			"reference": reference,
		}).Debug("Skipping metadata save for digest reference")
		return nil
	}

	s.log.WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
	}).Info("Saving Docker image metadata")

	// Calculate digest from manifest for metadata
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest for digest calculation: %w", err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	metadata := &models.ImageMetadata{
		Name:       name,
		Repository: name,
		Tag:        reference,
		Digest:     digest,
		Size:       manifest.GetTotalSize(),
		Created:    time.Now(),
		Layers:     s.extractLayerInfo(manifest),
	}

	if config, err := s.extractConfigFromBlob(manifest.Config.Digest); err == nil {
		metadata.Config = config
	}

	metadataPath := s.getMetadataPath(name, reference)
	metadataData, _ := json.MarshalIndent(metadata, "", "  ")
	if err := s.backend.Write(metadataPath, metadataData); err != nil {
		s.log.WithError(err).Warn("Failed to save metadata")
	}

	s.log.WithFields(logrus.Fields{
		"name":   name,
		"tag":    reference,
		"digest": digest,
		"size":   metadata.Size,
	}).Info("Docker image metadata saved successfully")

	return nil
}

// ListImages returns all available images grouped by name
func (s *ImageService) ListImages() ([]models.ImageGroup, error) {
	exists, err := s.backend.Exists("images")
	if err != nil || !exists {
		return []models.ImageGroup{}, nil
	}

	var allImages []models.ImageMetadata
	s.walkTagDirs("images", &allImages)

	return models.GroupImagesByName(allImages), nil
}

// walkTagDirs recursively walks directories under dir looking for "tags" subdirectories
func (s *ImageService) walkTagDirs(dir string, images *[]models.ImageMetadata) {
	entries, err := s.backend.List(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir {
			continue
		}

		fullPath := filepath.Join(dir, entry.Name)

		if entry.Name == "tags" {
			repoName := strings.TrimPrefix(dir, "images/")
			repoName = strings.TrimPrefix(repoName, "images\\")

			if strings.HasPrefix(repoName, "proxy/") || strings.HasPrefix(repoName, "proxy\\") {
				continue
			}

			s.processTagDir(repoName, fullPath, images)
		} else {
			s.walkTagDirs(fullPath, images)
		}
	}
}

func (s *ImageService) processTagDir(repoName, tagsDir string, images *[]models.ImageMetadata) {
	tags, err := s.backend.List(tagsDir)
	if err != nil {
		s.log.WithError(err).WithField("repo", repoName).Warn("Failed to read tags")
		return
	}

	for _, tagFile := range tags {
		if tagFile.IsDir || !strings.HasSuffix(tagFile.Name, ".json") {
			continue
		}

		tagName := strings.TrimSuffix(tagFile.Name, ".json")

		if strings.HasPrefix(tagName, "sha256") {
			continue
		}

		metadata, err := s.GetImageMetadata(repoName, tagName)
		if err != nil {
			s.log.WithError(err).WithFields(logrus.Fields{
				"repo": repoName,
				"tag":  tagName,
			}).Warn("Failed to get image metadata")
			continue
		}

		*images = append(*images, *metadata)
	}
}

// ImageExists checks if an image with the given name and tag exists
func (s *ImageService) ImageExists(name, tag string) bool {
	exists, _ := s.backend.Exists(s.getManifestPath(name, tag))
	return exists
}

// GetImageManifest returns the manifest for a specific image
func (s *ImageService) GetImageManifest(name, reference string) (*models.OCIManifest, error) {
	if strings.HasPrefix(reference, "sha256:") {
		return s.findManifestByDigest(name, reference)
	}

	manifestPath := s.getManifestPath(name, reference)
	data, err := s.backend.Read(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("manifest not found: %w", err)
	}

	var manifest models.OCIManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return &manifest, nil
}

// GetImageMetadata returns metadata for a specific image
func (s *ImageService) GetImageMetadata(name, tag string) (*models.ImageMetadata, error) {
	metadataPath := s.getMetadataPath(name, tag)

	data, err := s.backend.Read(metadataPath)
	if err != nil {
		manifest, err := s.GetImageManifest(name, tag)
		if err != nil {
			return nil, fmt.Errorf("image not found: %w", err)
		}

		return &models.ImageMetadata{
			Name:       name,
			Repository: name,
			Tag:        tag,
			Size:       manifest.GetTotalSize(),
			Layers:     s.extractLayerInfo(manifest),
		}, nil
	}

	var metadata models.ImageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	return &metadata, nil
}

// DeleteImage removes an image by name and tag
func (s *ImageService) DeleteImage(name, tag string) error {
	s.log.WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Info("Deleting Docker image")

	manifestPath := s.getManifestPath(name, tag)
	metadataPath := s.getMetadataPath(name, tag)

	manifestExists, _ := s.backend.Exists(manifestPath)
	metadataExists, _ := s.backend.Exists(metadataPath)

	if !manifestExists && !metadataExists {
		return fmt.Errorf("image not found: %s:%s", name, tag)
	}

	if manifestExists {
		if err := s.backend.Delete(manifestPath); err != nil {
			return fmt.Errorf("failed to delete manifest: %w", err)
		}
	}

	if metadataExists {
		if err := s.backend.Delete(metadataPath); err != nil {
			s.log.WithError(err).Warn("Failed to delete metadata")
		}
	}

	s.log.WithFields(logrus.Fields{
		"name": name,
		"tag":  tag,
	}).Info("Docker image deleted successfully")

	return nil
}

// GetImageConfig returns the parsed image configuration
func (s *ImageService) GetImageConfig(name, tag string) (*models.ImageConfig, error) {
	manifest, err := s.GetImageManifest(name, tag)
	if err != nil {
		return nil, err
	}

	return s.extractConfigFromBlob(manifest.Config.Digest)
}

// ListTags returns all tags for a given repository
func (s *ImageService) ListTags(name string) ([]string, error) {
	manifestsDir := filepath.Join("images", name, "manifests")

	exists, _ := s.backend.Exists(manifestsDir)
	if !exists {
		return []string{}, nil
	}

	files, err := s.backend.List(manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifests directory: %w", err)
	}

	var tags []string
	for _, f := range files {
		if f.IsDir {
			continue
		}
		name := f.Name
		if strings.HasPrefix(name, "sha256_") {
			continue
		}
		if strings.HasSuffix(name, ".json") {
			tags = append(tags, strings.TrimSuffix(name, ".json"))
		}
	}

	return tags, nil
}

// Helper functions

func (s *ImageService) getImageDir(name string) string {
	return filepath.Join("images", name)
}

func (s *ImageService) getManifestPath(name, reference string) string {
	safeRef := strings.ReplaceAll(reference, ":", "_")
	return filepath.Join("images", name, "manifests", safeRef+".json")
}

func (s *ImageService) getMetadataPath(name, tag string) string {
	return filepath.Join("images", name, "tags", tag+".json")
}

func (s *ImageService) extractLayerInfo(manifest *models.OCIManifest) []models.LayerInfo {
	layers := make([]models.LayerInfo, len(manifest.Layers))
	for i, layer := range manifest.Layers {
		layers[i] = models.LayerInfo{
			MediaType: layer.MediaType,
			Digest:    layer.Digest,
			Size:      layer.Size,
		}
	}
	return layers
}

func (s *ImageService) extractConfigFromBlob(digest string) (*models.ImageConfig, error) {
	blobPath := s.pathManager.GetBlobPath(digest)

	data, err := s.backend.Read(blobPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config blob: %w", err)
	}

	var config models.ImageConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	return &config, nil
}

// SaveImageIndex saves metadata for a manifest list/OCI index without corrupting the manifest data
func (s *ImageService) SaveImageIndex(name, reference string, manifestData []byte, totalSize int64) error {
	s.log.WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
		"size":      totalSize,
	}).Info("Saving Docker image index metadata")

	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	var index models.OCIIndex
	var platforms []models.PlatformInfo
	var config *models.ImageConfig
	var layers []models.LayerInfo

	if err := json.Unmarshal(manifestData, &index); err == nil {
		for _, m := range index.Manifests {
			if m.Platform != nil && m.Platform.OS != "unknown" && m.Platform.Architecture != "unknown" {
				platforms = append(platforms, models.PlatformInfo{
					OS:           m.Platform.OS,
					Architecture: m.Platform.Architecture,
					Variant:      m.Platform.Variant,
					Digest:       m.Digest,
				})
			}
		}

		config, layers = s.loadConfigFromManifestList(index)
	}

	metadata := &models.ImageMetadata{
		Name:       name,
		Repository: name,
		Tag:        reference,
		Digest:     digest,
		Size:       totalSize,
		Created:    time.Now(),
		Platforms:  platforms,
		Config:     config,
		Layers:     layers,
	}

	metadataPath := s.getMetadataPath(name, reference)
	metadataData, _ := json.MarshalIndent(metadata, "", "  ")
	if err := s.backend.Write(metadataPath, metadataData); err != nil {
		return fmt.Errorf("failed to save metadata: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name":      name,
		"tag":       reference,
		"digest":    digest,
		"size":      totalSize,
		"platforms": len(platforms),
	}).Info("Docker image index metadata saved successfully")

	return nil
}

func (s *ImageService) loadConfigFromManifestList(index models.OCIIndex) (*models.ImageConfig, []models.LayerInfo) {
	var targetDigest string
	for _, desc := range index.Manifests {
		if desc.Platform != nil && desc.Platform.OS == "linux" && desc.Platform.Architecture == "amd64" {
			targetDigest = desc.Digest
			break
		}
	}
	if targetDigest == "" && len(index.Manifests) > 0 {
		targetDigest = index.Manifests[0].Digest
	}
	if targetDigest == "" {
		return nil, nil
	}

	blobPath := s.pathManager.GetBlobPath(targetDigest)
	data, err := s.backend.Read(blobPath)
	if err != nil {
		s.log.WithError(err).WithField("digest", targetDigest).Debug("Could not read child manifest for config extraction")
		return nil, nil
	}

	var childManifest models.OCIManifest
	if err := json.Unmarshal(data, &childManifest); err != nil {
		s.log.WithError(err).Debug("Could not parse child manifest for config extraction")
		return nil, nil
	}

	layers := s.extractLayerInfo(&childManifest)

	config, err := s.extractConfigFromBlob(childManifest.Config.Digest)
	if err != nil {
		s.log.WithError(err).Debug("Could not extract config from child manifest")
		return nil, layers
	}

	return config, layers
}

func (s *ImageService) findManifestByDigest(name, digest string) (*models.OCIManifest, error) {
	manifestsDir := filepath.Join("images", name, "manifests")

	files, err := s.backend.List(manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifests directory: %w", err)
	}

	for _, f := range files {
		if f.IsDir {
			continue
		}

		filePath := filepath.Join(manifestsDir, f.Name)
		data, err := s.backend.Read(filePath)
		if err != nil {
			continue
		}

		currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		if currentDigest == digest {
			var manifest models.OCIManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return nil, fmt.Errorf("failed to parse manifest: %w", err)
			}
			return &manifest, nil
		}
	}

	return nil, fmt.Errorf("manifest with digest %s not found", digest)
}
