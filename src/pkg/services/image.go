// pkg/services/image.go
package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	utils "oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// ImageService handles Docker image operations
type ImageService struct {
	pathManager *utils.PathManager
	config      *config.Config
	log         *utils.Logger
}

// NewImageService creates a new image service
func NewImageService(config *config.Config, log *utils.Logger) *ImageService {
	pm := utils.NewPathManager(config.Storage.Path, log)

	// Create images directory
	imagesDir := filepath.Join(config.Storage.Path, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		log.WithError(err).Error("Failed to create images directory")
	}

	return &ImageService{
		pathManager: pm,
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
	s.log.WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
	}).Info("Saving Docker image metadata")

	// Create image directory
	imageDir := s.getImageDir(name)
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		return fmt.Errorf("failed to create image directory: %w", err)
	}

	// Calculate digest from manifest for metadata (note: this may differ from actual stored manifest)
	// The actual manifest with correct digest is stored by the handler
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest for digest calculation: %w", err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	// Create/update metadata
	metadata := &models.ImageMetadata{
		Name:       name,
		Repository: name,
		Tag:        reference,
		Digest:     digest,
		Size:       manifest.GetTotalSize(),
		Created:    time.Now(),
		Layers:     s.extractLayerInfo(manifest),
	}

	// Try to extract config if available
	if config, err := s.extractConfigFromBlob(manifest.Config.Digest); err == nil {
		metadata.Config = config
	}

	// Save metadata
	metadataPath := s.getMetadataPath(name, reference)
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		s.log.WithError(err).Warn("Failed to create metadata directory")
	}
	metadataData, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(metadataPath, metadataData, 0644); err != nil {
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
	imagesDir := filepath.Join(s.pathManager.GetBasePath(), "images")

	// Ensure directory exists
	if _, err := os.Stat(imagesDir); os.IsNotExist(err) {
		return []models.ImageGroup{}, nil
	}

	var allImages []models.ImageMetadata

	// Walk the images directory recursively to find all tags directories
	// This handles nested paths like library/alpine (docker.io official images)
	err := filepath.Walk(imagesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors and continue
		}

		// We're looking for "tags" directories
		if !info.IsDir() || info.Name() != "tags" {
			return nil
		}

		// Get the repository name by extracting the path between imagesDir and /tags
		relPath, err := filepath.Rel(imagesDir, filepath.Dir(path))
		if err != nil {
			return nil
		}
		repoName := relPath

		// Read tag files
		tags, err := os.ReadDir(path)
		if err != nil {
			s.log.WithError(err).WithField("repo", repoName).Warn("Failed to read tags")
			return nil
		}

		for _, tagFile := range tags {
			if tagFile.IsDir() || !strings.HasSuffix(tagFile.Name(), ".json") {
				continue
			}

			tagName := strings.TrimSuffix(tagFile.Name(), ".json")
			metadata, err := s.GetImageMetadata(repoName, tagName)
			if err != nil {
				s.log.WithError(err).WithFields(logrus.Fields{
					"repo": repoName,
					"tag":  tagName,
				}).Warn("Failed to get image metadata")
				continue
			}

			allImages = append(allImages, *metadata)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk images directory: %w", err)
	}

	return models.GroupImagesByName(allImages), nil
}

// ImageExists checks if an image with the given name and tag exists
func (s *ImageService) ImageExists(name, tag string) bool {
	manifestPath := s.getManifestPath(name, tag)
	_, err := os.Stat(manifestPath)
	return err == nil
}

// GetImageManifest returns the manifest for a specific image
func (s *ImageService) GetImageManifest(name, reference string) (*models.OCIManifest, error) {
	manifestPath := s.getManifestPath(name, reference)

	// If reference is a digest, try to find by scanning
	if strings.HasPrefix(reference, "sha256:") {
		return s.findManifestByDigest(name, reference)
	}

	data, err := os.ReadFile(manifestPath)
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

	data, err := os.ReadFile(metadataPath)
	if err != nil {
		// Try to reconstruct from manifest
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

	// Remove manifest
	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete manifest: %w", err)
	}

	// Remove metadata
	if err := os.Remove(metadataPath); err != nil && !os.IsNotExist(err) {
		s.log.WithError(err).Warn("Failed to delete metadata")
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
	manifestsDir := filepath.Join(s.pathManager.GetBasePath(), "images", name, "manifests")

	if _, err := os.Stat(manifestsDir); os.IsNotExist(err) {
		return []string{}, nil
	}

	files, err := os.ReadDir(manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifests directory: %w", err)
	}

	var tags []string
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		// Skip digest references
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
	return filepath.Join(s.pathManager.GetBasePath(), "images", name)
}

func (s *ImageService) getManifestPath(name, reference string) string {
	// Replace : with _ for filesystem compatibility
	safeRef := strings.ReplaceAll(reference, ":", "_")
	return filepath.Join(s.pathManager.GetBasePath(), "images", name, "manifests", safeRef+".json")
}

func (s *ImageService) getMetadataPath(name, tag string) string {
	return filepath.Join(s.pathManager.GetBasePath(), "images", name, "tags", tag+".json")
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

	data, err := os.ReadFile(blobPath)
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
// This is used for multi-arch images where we can't use SaveImage (which re-marshals as OCIManifest)
func (s *ImageService) SaveImageIndex(name, reference string, manifestData []byte, totalSize int64) error {
	s.log.WithFields(logrus.Fields{
		"name":      name,
		"reference": reference,
		"size":      totalSize,
	}).Info("Saving Docker image index metadata")

	// Create image directory
	imageDir := s.getImageDir(name)
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		return fmt.Errorf("failed to create image directory: %w", err)
	}

	// Calculate digest
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	// Create/update metadata for the image list
	metadata := &models.ImageMetadata{
		Name:       name,
		Repository: name,
		Tag:        reference,
		Digest:     digest,
		Size:       totalSize,
		Created:    time.Now(),
		Layers:     []models.LayerInfo{}, // Manifest lists don't have direct layers
	}

	// Save metadata to tags directory (this is what ListImages looks for)
	metadataPath := s.getMetadataPath(name, reference)
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0755); err != nil {
		return fmt.Errorf("failed to create metadata directory: %w", err)
	}
	metadataData, _ := json.MarshalIndent(metadata, "", "  ")
	if err := os.WriteFile(metadataPath, metadataData, 0644); err != nil {
		return fmt.Errorf("failed to save metadata: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name":   name,
		"tag":    reference,
		"digest": digest,
		"size":   totalSize,
	}).Info("Docker image index metadata saved successfully")

	return nil
}

func (s *ImageService) findManifestByDigest(name, digest string) (*models.OCIManifest, error) {
	manifestsDir := filepath.Join(s.pathManager.GetBasePath(), "images", name, "manifests")

	files, err := os.ReadDir(manifestsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifests directory: %w", err)
	}

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		filePath := filepath.Join(manifestsDir, f.Name())
		data, err := os.ReadFile(filePath)
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
