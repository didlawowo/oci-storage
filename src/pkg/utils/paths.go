// pkg/storage/paths.go
package utils

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"helm-portal/pkg/models"

	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
)

type PathManager struct {
	baseStoragePath string
	log             *Logger
}

func NewPathManager(basePath string, log *Logger) *PathManager {
	// Créer les dossiers nécessaires
	dirs := []string{
		"temp",            // Pour les uploads temporaires
		"blobs",           // Pour les blobs (shared between charts and images)
		"manifests",       // Pour les manifests Helm
		"charts",          // Pour les charts Helm
		"images",          // Pour les images Docker
		"cache/metadata",  // Pour les métadonnées du cache proxy
	}

	for _, dir := range dirs {
		path := filepath.Join(basePath, dir)
		if err := os.MkdirAll(path, 0755); err != nil {
			log.Fatalf("Failed to create directory %s: %v", path, err)
		}
	}

	return &PathManager{
		baseStoragePath: basePath,
		log:             log,
	}
}

func (pm *PathManager) GetTempPath(uuid string) string {
	return filepath.Join(pm.baseStoragePath, "temp", uuid)
}

func (pm *PathManager) GetBlobPath(digest string) string {
	return filepath.Join(pm.baseStoragePath, "blobs", digest)
}

func (pm *PathManager) GetManifestPath(name, reference string) string {
	reference = reference + ".json"
	return filepath.Join(pm.baseStoragePath, "manifests", name, reference)
}

func (pm *PathManager) GetChartPath(chartName string, reference string) string {
	// Si c'est un digest SHA256
	if strings.HasPrefix(reference, "sha256:") {
		// On cherche d'abord dans les manifests pour obtenir la version
		manifestPath := filepath.Join(pm.baseStoragePath, "manifests", chartName)
		manifestFile := filepath.Join(manifestPath, "0.2.0.json") // Pour le moment en dur

		// Lire le manifest
		content, err := os.ReadFile(manifestFile)
		if err == nil {
			// Parse le json pour obtenir la version
			var manifest models.OCIManifest // Ajoutez la structure OCIManifest
			if err := json.Unmarshal(content, &manifest); err == nil {
				// Utiliser la version du manifest plutôt que le digest
				return filepath.Join(pm.baseStoragePath, "charts", fmt.Sprintf("%s-%s.tgz", chartName, "0.2.0"))
			}
		}
	}

	// Sinon c'est une version normale
	return filepath.Join(pm.baseStoragePath, "charts", fmt.Sprintf("%s-%s.tgz", chartName, reference))
}

func (pm *PathManager) FindManifestByDigest(chartName string, digest string) string {
	// Le manifest est toujours dans manifests/chartName/version.json
	manifestPath := filepath.Join(pm.baseStoragePath, "manifests", chartName, "0.2.0.json")

	pm.log.WithFields(logrus.Fields{
		"manifestPath": manifestPath,
		"chartName":    chartName,
		"digest":       digest,
	}).Debug("Looking for manifest")

	// Vérifier le digest
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		pm.log.WithError(err).Error("Failed to read manifest")
		return ""
	}

	currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	if currentDigest == digest {
		return manifestPath
	}

	return ""
}

func (pm *PathManager) GetBasePath() string {
	return filepath.Join(pm.baseStoragePath)
}

func (pm *PathManager) GetChartsPath() string {
	return filepath.Join(pm.baseStoragePath, "charts")
}

func (pm *PathManager) GetOCIRepositoryPath(name string) string {
	return filepath.Join(pm.baseStoragePath, "oci", name)
}

func (pm *PathManager) GetIndexPath() string {
	return filepath.Join(pm.baseStoragePath, "index.yaml")
}

// Docker image path helpers

func (pm *PathManager) GetImagesPath() string {
	return filepath.Join(pm.baseStoragePath, "images")
}

func (pm *PathManager) GetImagePath(name string) string {
	return filepath.Join(pm.baseStoragePath, "images", name)
}

func (pm *PathManager) GetImageManifestPath(name, reference string) string {
	// Replace : with _ for filesystem compatibility (sha256:xxx -> sha256_xxx)
	safeRef := strings.ReplaceAll(reference, ":", "_")
	return filepath.Join(pm.baseStoragePath, "images", name, "manifests", safeRef+".json")
}

func (pm *PathManager) GetImageTagPath(name, tag string) string {
	return filepath.Join(pm.baseStoragePath, "images", name, "tags", tag+".json")
}

// Cache path helpers for proxy functionality

func (pm *PathManager) GetCachePath() string {
	return filepath.Join(pm.baseStoragePath, "cache")
}

func (pm *PathManager) GetCacheMetadataPath() string {
	return filepath.Join(pm.baseStoragePath, "cache", "metadata")
}

func (pm *PathManager) GetCacheStatePath() string {
	return filepath.Join(pm.baseStoragePath, "cache", "state.json")
}

func (pm *PathManager) GetCachedImageMetadataPath(name, tag string) string {
	// Replace / with _ for filesystem compatibility (library/alpine -> library_alpine)
	safeName := strings.ReplaceAll(name, "/", "_")
	return filepath.Join(pm.baseStoragePath, "cache", "metadata", safeName+"_"+tag+".json")
}
