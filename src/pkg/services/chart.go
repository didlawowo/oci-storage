// internal/chart/service/chart_service.go

package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strings"

	"oci-storage/config"
	"oci-storage/pkg/models"
	utils "oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type IndexUpdater interface {
	UpdateIndex() error
	EnsureIndexExists() error
	GetIndexPath() string
}

// ChartService handles chart operations
type ChartService struct {
	pathManager *utils.PathManager
	config      *config.Config
	log         *utils.Logger

	indexUpdater IndexUpdater
}

// NewChartService creates a new chart service
func NewChartService(config *config.Config, log *utils.Logger, indexUpdater IndexUpdater) *ChartService {
	if err := os.MkdirAll(config.Storage.Path, 0755); err != nil {
		log.WithError(err).Error("❌ Impossible de créer le dossier de stockage")
	}
	return &ChartService{
		pathManager:  utils.NewPathManager(config.Storage.Path, log),
		config:       config,
		log:          log,
		indexUpdater: indexUpdater,
	}
}
func (s *ChartService) GetPathManager() *utils.PathManager {
	return s.pathManager
}

// SaveChart saves an uploaded chart file
func (s *ChartService) SaveChart(chartData []byte, filename string) error {
	// ✨ Create charts directory if not exists
	chartsDir := s.pathManager.GetChartsPath()

	// 💾 Save chart file
	chartPath := filepath.Join(chartsDir, filename)
	if err := os.WriteFile(chartPath, chartData, 0644); err != nil {
		return fmt.Errorf("❌ failed to save chart: %w", err)
	}

	// 📝 Extract and validate metadata
	metadata, err := s.ExtractChartMetadata(chartData)
	if err != nil {
		return fmt.Errorf("❌ failed to extract chart metadata: %w", err)
	}

	if err := s.indexUpdater.UpdateIndex(); err != nil {
		s.log.WithError(err).Error("❌ Échec mise à jour index")
		return fmt.Errorf("échec mise à jour index: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name":    metadata.Name,
		"version": metadata.Version,
		"file":    filename,
	}).Info("✅ Chart saved successfully")

	return nil
}

// extractChartMetadata extracts Chart.yaml from the tgz file
func (s *ChartService) ExtractChartMetadata(chartData []byte) (*models.ChartMetadata, error) {
	// 📦 Read the gzip file
	gr, err := gzip.NewReader(bytes.NewReader(chartData))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	// 📂 Read the tar archive
	tr := tar.NewReader(gr)

	// 🔍 Look for Chart.yaml
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		// Find Chart.yaml in the root directory of the chart
		if filepath.Base(header.Name) == "Chart.yaml" {
			// Read the Chart.yaml content
			content, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}

			// Parse YAML
			var metadata models.ChartMetadata
			if err := yaml.Unmarshal(content, &metadata); err != nil {
				return nil, err
			}

			return &metadata, nil
		}
	}

	return nil, fmt.Errorf("Chart.yaml not found in chart archive")
}

// ListCharts returns all available charts grouped by name with their versions
func (s *ChartService) ListCharts() ([]models.ChartGroup, error) {
	chartsDir := s.pathManager.GetChartsPath()
	var chartMetadatas []models.ChartMetadata

	// Read charts directory
	files, err := os.ReadDir(chartsDir)
	if err != nil {
		return nil, err
	}

	// Process each .tgz file
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".tgz") {
			continue
		}

		// Read chart data
		chartData, err := os.ReadFile(filepath.Join(chartsDir, file.Name()))
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name()).Error("Failed to read chart")
			continue
		}

		// Extract metadata
		metadata, err := s.ExtractChartMetadata(chartData)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name()).Error("Failed to extract metadata")
			continue
		}

		chartMetadatas = append(chartMetadatas, *metadata)
		// trier les charts par nom et version TODO

	}

	// Utiliser GroupChartsByName pour grouper les charts
	return models.GroupChartsByName(chartMetadatas), nil
}

func (s *ChartService) ChartExists(chartName string, version string) bool {
	_, err := os.Stat(s.pathManager.GetChartPath(chartName, version))
	return !os.IsNotExist(err)
}

func (s *ChartService) GetChart(chartName string, version string) ([]byte, error) {
	chartPath := s.pathManager.GetChartPath(chartName, version)
	// Vérifier si le chart existe
	if !s.ChartExists(chartName, version) {
		return nil, fmt.Errorf("chart %s version %s not found", chartName, version)
	}

	return os.ReadFile(chartPath)
}

func (s *ChartService) GetChartDetails(chartName string, version string) (*models.ChartMetadata, error) {
	chartPath := s.pathManager.GetChartPath(chartName, version)
	// Vérifier si le chart existe
	if !s.ChartExists(chartName, version) {
		return nil, fmt.Errorf("chart %s version %s not found", chartName, version)
	}
	chartData, err := os.ReadFile(chartPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read chart: %w", err)
	}
	// Extract metadata
	metadata, err := s.ExtractChartMetadata(chartData)
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata: %w", err)
	}
	return metadata, nil
}

func (s *ChartService) DeleteChart(chartName string, version string) error {
	chartPath := s.pathManager.GetChartPath(chartName, version)

	// Vérifier si le chart existe
	if !s.ChartExists(chartName, version) {
		return fmt.Errorf("chart %s version %s not found", chartName, version)
	}

	// Supprimer le fichier
	if err := os.Remove(chartPath); err != nil {
		return fmt.Errorf("failed to delete chart: %w", err)
	}

	// Mettre à jour l'index
	return s.indexUpdater.UpdateIndex()
}

func (s *ChartService) GetChartValues(chartName string, version string) (string, error) {
	// 📂 Récupérer le chemin du chart
	chartPath := s.pathManager.GetChartPath(chartName, version)

	// 📦 Ouvrir le fichier tgz
	f, err := os.Open(chartPath)
	if err != nil {
		return "", fmt.Errorf("❌ failed to open chart file: %w", err)
	}
	defer f.Close()

	// 🗜️ Lire le contenu du tgz
	gzf, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("❌ failed to create gzip reader: %w", err)
	}
	defer gzf.Close()

	// 📄 Lire le tar
	tr := tar.NewReader(gzf)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("❌ failed to read tar: %w", err)
		}

		// 🔍 Chercher values.yaml
		if strings.HasSuffix(header.Name, "values.yaml") {
			content, err := io.ReadAll(tr)
			if err != nil {
				return "", fmt.Errorf("❌ failed to read values.yaml: %w", err)
			}
			return string(content), nil
		}
	}

	return "", fmt.Errorf("❌ values.yaml not found in chart")
}
