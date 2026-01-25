// pkg/utils/paths.go
package utils

import (
	"crypto/sha256"
	"fmt"
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
	// Si c'est un digest SHA256, on doit trouver la version correspondante
	if strings.HasPrefix(reference, "sha256:") {
		manifestsDir := filepath.Join(pm.baseStoragePath, "manifests", chartName)

		// Scanner tous les manifests pour trouver celui qui correspond au digest
		files, err := os.ReadDir(manifestsDir)
		if err != nil {
			pm.log.WithError(err).Debug("Failed to read manifests directory")
			return ""
		}

		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}

			manifestFile := filepath.Join(manifestsDir, file.Name())
			content, err := os.ReadFile(manifestFile)
			if err != nil {
				continue
			}

			// Calculer le digest du manifest
			currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
			if currentDigest == reference {
				// Extraire la version du nom de fichier (ex: "1.0.0.json" -> "1.0.0")
				version := strings.TrimSuffix(file.Name(), ".json")
				return filepath.Join(pm.baseStoragePath, "charts", fmt.Sprintf("%s-%s.tgz", chartName, version))
			}
		}

		pm.log.WithFields(logrus.Fields{
			"chartName": chartName,
			"digest":    reference,
		}).Debug("No manifest found for digest")
		return ""
	}

	// Sinon c'est une version normale
	return filepath.Join(pm.baseStoragePath, "charts", fmt.Sprintf("%s-%s.tgz", chartName, reference))
}

func (pm *PathManager) FindManifestByDigest(chartName string, digest string) string {
	manifestsDir := filepath.Join(pm.baseStoragePath, "manifests", chartName)

	pm.log.WithFields(logrus.Fields{
		"manifestsDir": manifestsDir,
		"chartName":    chartName,
		"digest":       digest,
	}).Debug("Looking for manifest by digest")

	// Scanner tous les manifests pour trouver celui qui correspond au digest
	files, err := os.ReadDir(manifestsDir)
	if err != nil {
		pm.log.WithError(err).Debug("Failed to read manifests directory")
		return ""
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		manifestPath := filepath.Join(manifestsDir, file.Name())
		content, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		currentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if currentDigest == digest {
			return manifestPath
		}
	}

	pm.log.WithFields(logrus.Fields{
		"chartName": chartName,
		"digest":    digest,
	}).Debug("No manifest found for digest")
	return ""
}

func (pm *PathManager) GetBasePath() string {
	return filepath.Join(pm.baseStoragePath)
}

func (pm *PathManager) GetChartsPath() string {
	return filepath.Join(pm.baseStoragePath, "charts")
}

func (pm *PathManager) GetIndexPath() string {
	return filepath.Join(pm.baseStoragePath, "index.yaml")
}

func (pm *PathManager) GetImageManifestPath(name, reference string) string {
	// Replace : with _ for filesystem compatibility (sha256:xxx -> sha256_xxx)
	safeRef := strings.ReplaceAll(reference, ":", "_")
	return filepath.Join(pm.baseStoragePath, "images", name, "manifests", safeRef+".json")
}

func (pm *PathManager) GetCacheStatePath() string {
	return filepath.Join(pm.baseStoragePath, "cache", "state.json")
}

func (pm *PathManager) GetCachedImageMetadataPath(name, tag string) string {
	// Replace / with _ for filesystem compatibility (library/alpine -> library_alpine)
	safeName := strings.ReplaceAll(name, "/", "_")
	return filepath.Join(pm.baseStoragePath, "cache", "metadata", safeName+"_"+tag+".json")
}
