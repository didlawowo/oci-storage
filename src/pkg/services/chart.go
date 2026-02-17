// internal/chart/service/chart_service.go

package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/storage"
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
	backend     storage.Backend
	config      *config.Config
	log         *utils.Logger

	indexUpdater IndexUpdater
}

// NewChartService creates a new chart service
func NewChartService(config *config.Config, log *utils.Logger, pm *utils.PathManager, backend storage.Backend, indexUpdater IndexUpdater) *ChartService {
	return &ChartService{
		pathManager:  pm,
		backend:      backend,
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
	chartPath := filepath.Join(s.pathManager.GetChartsPath(), filename)
	if err := s.backend.Write(chartPath, chartData); err != nil {
		return fmt.Errorf("failed to save chart: %w", err)
	}

	metadata, err := s.ExtractChartMetadata(chartData)
	if err != nil {
		return fmt.Errorf("failed to extract chart metadata: %w", err)
	}

	if err := s.indexUpdater.UpdateIndex(); err != nil {
		s.log.WithError(err).Error("Failed to update index")
		return fmt.Errorf("failed to update index: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"name":    metadata.Name,
		"version": metadata.Version,
		"file":    filename,
	}).Info("Chart saved successfully")

	return nil
}

// ExtractChartMetadata extracts Chart.yaml from the tgz file
func (s *ChartService) ExtractChartMetadata(chartData []byte) (*models.ChartMetadata, error) {
	gr, err := gzip.NewReader(bytes.NewReader(chartData))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if filepath.Base(header.Name) == "Chart.yaml" {
			content, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}

			var metadata models.ChartMetadata
			if err := yaml.Unmarshal(content, &metadata); err != nil {
				return nil, err
			}

			return &metadata, nil
		}
	}

	return nil, fmt.Errorf("chart.yaml not found in chart archive")
}

// ListCharts returns all available charts grouped by name with their versions
func (s *ChartService) ListCharts() ([]models.ChartGroup, error) {
	chartsDir := s.pathManager.GetChartsPath()
	var chartMetadatas []models.ChartMetadata

	files, err := s.backend.List(chartsDir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !strings.HasSuffix(file.Name, ".tgz") {
			continue
		}

		chartData, err := s.backend.Read(filepath.Join(chartsDir, file.Name))
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name).Error("Failed to read chart")
			continue
		}

		metadata, err := s.ExtractChartMetadata(chartData)
		if err != nil {
			s.log.WithError(err).WithField("file", file.Name).Error("Failed to extract metadata")
			continue
		}

		chartMetadatas = append(chartMetadatas, *metadata)
	}

	return models.GroupChartsByName(chartMetadatas), nil
}

func (s *ChartService) ChartExists(chartName string, version string) bool {
	exists, _ := s.backend.Exists(s.pathManager.GetChartPath(chartName, version))
	return exists
}

func (s *ChartService) GetChart(chartName string, version string) ([]byte, error) {
	chartPath := s.pathManager.GetChartPath(chartName, version)
	if !s.ChartExists(chartName, version) {
		return nil, fmt.Errorf("chart %s version %s not found", chartName, version)
	}

	return s.backend.Read(chartPath)
}

func (s *ChartService) GetChartDetails(chartName string, version string) (*models.ChartMetadata, error) {
	if !s.ChartExists(chartName, version) {
		return nil, fmt.Errorf("chart %s version %s not found", chartName, version)
	}
	chartData, err := s.backend.Read(s.pathManager.GetChartPath(chartName, version))
	if err != nil {
		return nil, fmt.Errorf("failed to read chart: %w", err)
	}
	metadata, err := s.ExtractChartMetadata(chartData)
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata: %w", err)
	}
	return metadata, nil
}

// ListChartVersions returns all available versions for a specific chart
func (s *ChartService) ListChartVersions(chartName string) ([]string, error) {
	chartsDir := s.pathManager.GetChartsPath()
	var versions []string

	files, err := s.backend.List(chartsDir)
	if err != nil {
		return nil, err
	}

	prefix := chartName + "-"
	for _, file := range files {
		name := file.Name
		if !strings.HasSuffix(name, ".tgz") || !strings.HasPrefix(name, prefix) {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".tgz")
		versions = append(versions, version)
	}

	sort.Sort(sort.Reverse(sort.StringSlice(versions)))

	return versions, nil
}

func (s *ChartService) DeleteChart(chartName string, version string) error {
	chartPath := s.pathManager.GetChartPath(chartName, version)

	if !s.ChartExists(chartName, version) {
		return fmt.Errorf("chart %s version %s not found", chartName, version)
	}

	if err := s.backend.Delete(chartPath); err != nil {
		return fmt.Errorf("failed to delete chart: %w", err)
	}

	return s.indexUpdater.UpdateIndex()
}

func (s *ChartService) GetChartValues(chartName string, version string) (string, error) {
	chartPath := s.pathManager.GetChartPath(chartName, version)

	reader, err := s.backend.ReadStream(chartPath)
	if err != nil {
		return "", fmt.Errorf("failed to open chart file: %w", err)
	}
	defer reader.Close()

	gzf, err := gzip.NewReader(reader)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzf.Close()

	tr := tar.NewReader(gzf)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("failed to read tar: %w", err)
		}

		if strings.HasSuffix(header.Name, "values.yaml") {
			content, err := io.ReadAll(tr)
			if err != nil {
				return "", fmt.Errorf("failed to read values.yaml: %w", err)
			}
			return string(content), nil
		}
	}

	return "", fmt.Errorf("values.yaml not found in chart")
}
