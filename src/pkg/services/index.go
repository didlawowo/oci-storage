// internal/chart/service/index.go

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	"oci-storage/config"

	"oci-storage/pkg/interfaces"
	"oci-storage/pkg/storage"
	utils "oci-storage/pkg/utils"

	"time"
)

// IndexFile représente la structure de index.yaml
type IndexFile struct {
	APIVersion string                     `yaml:"apiVersion"`
	Generated  time.Time                  `yaml:"generated"`
	Entries    map[string][]*ChartVersion `yaml:"entries"`
}

// ChartVersion représente une version spécifique d'un chart
type ChartVersion struct {
	Name        string    `yaml:"name"`
	Version     string    `yaml:"version"`
	Description string    `yaml:"description"`
	AppVersion  string    `yaml:"appVersion,omitempty"`
	APIVersion  string    `yaml:"apiVersion,omitempty"`
	Created     time.Time `yaml:"created"`
	Digest      string    `yaml:"digest"` // SHA256 du fichier
	URLs        []string  `yaml:"urls"`   // URLs de téléchargement
}

type IndexService struct {
	pathManager  *utils.PathManager
	backend      storage.Backend
	config       *config.Config
	log          *utils.Logger
	baseURL      string
	chartService interfaces.ChartServiceInterface
}

// GetIndexPath returns the path to index.yaml
func (s *IndexService) GetIndexPath() string {
	return s.pathManager.GetIndexPath()
}

func NewIndexService(config *config.Config, log *utils.Logger, pm *utils.PathManager, backend storage.Backend, chartService interfaces.ChartServiceInterface) *IndexService {
	return &IndexService{
		pathManager:  pm,
		backend:      backend,
		config:       config,
		log:          log,
		chartService: chartService,
	}
}

func (s *IndexService) EnsureIndexExists() error {
	indexPath := s.pathManager.GetIndexPath()
	exists, _ := s.backend.Exists(indexPath)
	if !exists {
		return s.UpdateIndex()
	}
	return nil
}

// generateIndex crée ou met à jour le fichier index.yaml
func (s *IndexService) UpdateIndex() error {
	s.log.Info("Generating index.yaml")

	// Créer un nouvel index
	index := &IndexFile{
		APIVersion: "v1",
		Generated:  time.Now(),
		Entries:    make(map[string][]*ChartVersion),
	}

	// Lire le répertoire des charts
	chartsDir := s.pathManager.GetChartsPath()
	files, err := s.backend.List(chartsDir)
	if err != nil {
		return fmt.Errorf("error reading charts directory: %w", err)
	}

	// Traiter chaque fichier .tgz
	for _, file := range files {
		if filepath.Ext(file.Name) != ".tgz" {
			continue
		}

		// Lire le fichier chart
		chartPath := filepath.Join(chartsDir, file.Name)
		chartData, err := s.backend.Read(chartPath)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name).Error("Error reading chart")
			continue
		}

		// Extraire les métadonnées
		metadata, err := s.chartService.ExtractChartMetadata(chartData)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name).Error("Error extracting metadata")
			continue
		}

		// Calculer le digest SHA256
		digest := sha256.Sum256(chartData)
		digestStr := hex.EncodeToString(digest[:])

		// Créer l'URL de téléchargement
		downloadURL := fmt.Sprintf("%s/charts/%s", s.baseURL, file.Name)

		// Créer la version du chart
		chartVersion := &ChartVersion{
			Name:        metadata.Name,
			Version:     metadata.Version,
			Description: metadata.Description,
			AppVersion:  metadata.AppVersion,
			APIVersion:  metadata.ApiVersion,
			Created:     time.Now(),
			Digest:      digestStr,
			URLs:        []string{downloadURL},
		}

		// Ajouter à l'index
		if _, exists := index.Entries[metadata.Name]; !exists {
			index.Entries[metadata.Name] = []*ChartVersion{}
		}
		index.Entries[metadata.Name] = append(index.Entries[metadata.Name], chartVersion)

		s.log.WithFields(logrus.Fields{
			"name":    metadata.Name,
			"version": metadata.Version,
			"digest":  digestStr[:8],
		}).Debug("Chart added to index")
	}

	indexYAML, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("error marshaling index: %w", err)
	}

	indexPath := s.pathManager.GetIndexPath()
	if err := s.backend.Write(indexPath, indexYAML); err != nil {
		return fmt.Errorf("error saving index: %w", err)
	}

	s.log.Info("Index.yaml generated successfully")
	return nil
}
