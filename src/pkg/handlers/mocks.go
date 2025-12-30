// pkg/handlers/mocks.go
package handlers

import (
	"context"
	"io"

	"helm-portal/pkg/models"
	utils "helm-portal/pkg/utils"

	"github.com/stretchr/testify/mock"
)

type MockChartService struct {
	mock.Mock
}

func (m *MockChartService) SaveChart(chartData []byte, filename string) error {
	args := m.Called(chartData, filename)
	return args.Error(0)
}

func (m *MockChartService) GetChart(name string, version string) ([]byte, error) {
	args := m.Called(name, version)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockChartService) DeleteChart(name string, version string) error {
	args := m.Called(name, version)
	return args.Error(0)
}

func (m *MockChartService) ListCharts() ([]models.ChartGroup, error) { // Correction ici
	args := m.Called()
	return args.Get(0).([]models.ChartGroup), args.Error(1)
}

func (m *MockChartService) ChartExists(name string, version string) bool {
	args := m.Called(name, version)
	return args.Bool(0)
}

func (m *MockChartService) GetPathManager() *utils.PathManager {
	args := m.Called()
	return args.Get(0).(*utils.PathManager)
}

func (m *MockChartService) GetChartValues(name, version string) (string, error) {
	args := m.Called(name, version)
	return args.String(0), args.Error(1)
}

// Nouvelles méthodes
func (m *MockChartService) GetChartDetails(name, version string) (*models.ChartMetadata, error) {
	args := m.Called(name, version)
	return args.Get(0).(*models.ChartMetadata), args.Error(1)
}

func (m *MockChartService) ExtractChartMetadata(chartData []byte) (*models.ChartMetadata, error) {
	args := m.Called(chartData)
	return args.Get(0).(*models.ChartMetadata), args.Error(1)
}

// MockProxyService implements ProxyServiceInterface for testing
type MockProxyService struct {
	mock.Mock
}

func (m *MockProxyService) ResolveRegistry(imagePath string) (string, string, error) {
	args := m.Called(imagePath)
	return args.String(0), args.String(1), args.Error(2)
}

func (m *MockProxyService) GetDefaultRegistry() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockProxyService) GetManifest(ctx context.Context, registryURL, name, reference string) ([]byte, string, error) {
	args := m.Called(ctx, registryURL, name, reference)
	if args.Get(0) == nil {
		return nil, args.String(1), args.Error(2)
	}
	return args.Get(0).([]byte), args.String(1), args.Error(2)
}

func (m *MockProxyService) GetBlob(ctx context.Context, registryURL, name, digest string) (io.ReadCloser, int64, error) {
	args := m.Called(ctx, registryURL, name, digest)
	if args.Get(0) == nil {
		return nil, args.Get(1).(int64), args.Error(2)
	}
	return args.Get(0).(io.ReadCloser), args.Get(1).(int64), args.Error(2)
}

func (m *MockProxyService) GetCacheState() *models.CacheState {
	args := m.Called()
	return args.Get(0).(*models.CacheState)
}

func (m *MockProxyService) GetCachedImages() ([]models.CachedImageMetadata, error) {
	args := m.Called()
	return args.Get(0).([]models.CachedImageMetadata), args.Error(1)
}

func (m *MockProxyService) UpdateAccessTime(name, tag string) {
	m.Called(name, tag)
}

func (m *MockProxyService) EvictLRU(targetBytes int64) error {
	args := m.Called(targetBytes)
	return args.Error(0)
}

func (m *MockProxyService) DeleteCachedImage(name, tag string) error {
	args := m.Called(name, tag)
	return args.Error(0)
}

func (m *MockProxyService) AddToCache(metadata models.CachedImageMetadata) error {
	args := m.Called(metadata)
	return args.Error(0)
}

func (m *MockProxyService) IsEnabled() bool {
	args := m.Called()
	return args.Bool(0)
}

// MockImageService implements ImageServiceInterface for testing
type MockImageService struct {
	mock.Mock
}

func (m *MockImageService) SaveImage(name, reference string, manifest *models.OCIManifest) error {
	args := m.Called(name, reference, manifest)
	return args.Error(0)
}

func (m *MockImageService) ListImages() ([]models.ImageGroup, error) {
	args := m.Called()
	return args.Get(0).([]models.ImageGroup), args.Error(1)
}

func (m *MockImageService) ImageExists(name, tag string) bool {
	args := m.Called(name, tag)
	return args.Bool(0)
}

func (m *MockImageService) GetImageManifest(name, reference string) (*models.OCIManifest, error) {
	args := m.Called(name, reference)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.OCIManifest), args.Error(1)
}

func (m *MockImageService) GetImageMetadata(name, tag string) (*models.ImageMetadata, error) {
	args := m.Called(name, tag)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.ImageMetadata), args.Error(1)
}

func (m *MockImageService) DeleteImage(name, tag string) error {
	args := m.Called(name, tag)
	return args.Error(0)
}

func (m *MockImageService) GetImageConfig(name, tag string) (*models.ImageConfig, error) {
	args := m.Called(name, tag)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*models.ImageConfig), args.Error(1)
}

func (m *MockImageService) ListTags(name string) ([]string, error) {
	args := m.Called(name)
	return args.Get(0).([]string), args.Error(1)
}

func (m *MockImageService) GetPathManager() *utils.PathManager {
	args := m.Called()
	return args.Get(0).(*utils.PathManager)
}
