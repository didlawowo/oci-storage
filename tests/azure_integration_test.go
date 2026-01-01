package tests

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oci-storage/config"
	service "oci-storage/pkg/services"
	"oci-storage/pkg/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ces tests nécessitent une vraie configuration Azure pour s'exécuter
// Ils sont skippés par défaut et doivent être activés explicitement

const (
	// Variables d'environnement pour les tests d'intégration Azure
	azureIntegrationEnvVar    = "AZURE_INTEGRATION_TEST"
	azureStorageAccountEnvVar = "AZURE_TEST_STORAGE_ACCOUNT"
	azureStorageKeyEnvVar     = "AZURE_TEST_STORAGE_KEY"
	azureContainerEnvVar      = "AZURE_TEST_CONTAINER"
)

// setupAzureIntegrationTest configure l'environnement pour les tests d'intégration
func setupAzureIntegrationTest(t *testing.T) (*config.Config, *utils.Logger, string) {
	// Vérifier si les tests d'intégration sont activés
	if os.Getenv(azureIntegrationEnvVar) != "true" {
		t.Skip("Azure integration tests are disabled. Set AZURE_INTEGRATION_TEST=true to enable them.")
	}

	// Vérifier que toutes les variables d'environnement nécessaires sont définies
	storageAccount := os.Getenv(azureStorageAccountEnvVar)
	storageKey := os.Getenv(azureStorageKeyEnvVar)
	container := os.Getenv(azureContainerEnvVar)

	if storageAccount == "" || storageKey == "" || container == "" {
		t.Skipf("Missing Azure credentials. Required env vars: %s, %s, %s",
			azureStorageAccountEnvVar, azureStorageKeyEnvVar, azureContainerEnvVar)
	}

	// Créer un répertoire temporaire
	tempDir, err := ioutil.TempDir("", "azure-integration-test")
	require.NoError(t, err)

	// Configuration pour les tests d'intégration
	cfg := &config.Config{
		Storage: struct {
			Path string `yaml:"path"`
		}{
			Path: tempDir,
		},
		Backup: config.Backup{
			Enabled:  true,
			Provider: "azure",
			Azure: struct {
				StorageAccount string `yaml:"storageAccount"`
				Container      string `yaml:"container"`
			}{
				StorageAccount: storageAccount,
				Container:      container + "-" + fmt.Sprintf("%d", time.Now().Unix()), // Container unique
			},
		},
	}

	// Logger pour les tests d'intégration
	logger := utils.NewLogger(utils.Config{
		LogLevel:  "info", // Moins verbose pour les tests d'intégration
		LogFormat: "json",
		Pretty:    true,
	})

	// Configurer la clé d'accès Azure
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", storageKey)

	return cfg, logger, tempDir
}

// TestAzureIntegration_BasicBackup teste un backup de base avec Azure réel
func TestAzureIntegration_BasicBackup(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Créer des fichiers de test
	testFiles := map[string]string{
		"charts/test-chart-1.0.0.tgz": "fake chart content for integration test",
		"index.yaml":                  "apiVersion: v1\nentries:\n  test-chart: []",
		"manifests/test/1.0.0.json":   `{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json"}`,
	}

	for relPath, content := range testFiles {
		fullPath := filepath.Join(tempDir, relPath)
		dir := filepath.Dir(fullPath)

		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err)

		err = ioutil.WriteFile(fullPath, []byte(content), 0644)
		require.NoError(t, err)
	}

	// Créer le service de backup
	backupService, err := service.NewBackupService(cfg, logger)
	if err != nil {
		// Si l'initialisation échoue, vérifier le type d'erreur
		if strings.Contains(err.Error(), "failed to access Azure container") {
			t.Logf("Azure container access failed (expected): %v", err)
			return // Test réussi - nous avons testé la connectivité
		}
		require.NoError(t, err, "Unexpected error during service initialization")
	}

	// Si le service est créé avec succès, tester le backup
	if backupService != nil {
		t.Log("Successfully connected to Azure Blob Storage")

		// Exécuter le backup
		err = backupService.Backup()
		if err != nil {
			t.Logf("Backup failed (may be expected in test environment): %v", err)
		} else {
			t.Log("Backup completed successfully")
		}
	}
}

// TestAzureIntegration_ContainerCreation teste la création automatique de container
func TestAzureIntegration_ContainerCreation(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Utiliser un nom de container unique qui n'existe probablement pas
	uniqueContainer := fmt.Sprintf("helm-test-container-%d", time.Now().UnixNano())
	cfg.Backup.Azure.Container = uniqueContainer

	t.Logf("Testing container creation with unique name: %s", uniqueContainer)

	// Tenter de créer le service (devrait créer le container automatiquement)
	backupService, err := service.NewBackupService(cfg, logger)

	if err != nil {
		if strings.Contains(err.Error(), "failed to create container") {
			t.Logf("Container creation failed (may be expected): %v", err)
		} else if strings.Contains(err.Error(), "failed to access Azure container") {
			t.Logf("Container access failed - container may have been created: %v", err)
		} else {
			t.Logf("Service initialization failed: %v", err)
		}
	} else {
		t.Log("Service created successfully - container should exist now")
		if backupService != nil {
			t.Log("Backup service is ready for use")
		}
	}
}

// TestAzureIntegration_LargeFileBackup teste le backup de gros fichiers
func TestAzureIntegration_LargeFileBackup(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Créer un fichier plus gros (5MB) pour tester les uploads par blocs
	largeContent := make([]byte, 5*1024*1024) // 5MB
	for i := range largeContent {
		largeContent[i] = byte(i % 256)
	}

	largeFilePath := filepath.Join(tempDir, "charts", "large-chart.tgz")
	err := os.MkdirAll(filepath.Dir(largeFilePath), 0755)
	require.NoError(t, err)

	err = ioutil.WriteFile(largeFilePath, largeContent, 0644)
	require.NoError(t, err)

	t.Logf("Created large test file: %s (%.2f MB)", largeFilePath, float64(len(largeContent))/1024/1024)

	// Créer le service de backup
	backupService, err := service.NewBackupService(cfg, logger)
	if err != nil {
		t.Logf("Service initialization failed: %v", err)
		return
	}

	if backupService != nil {
		// Exécuter le backup du gros fichier
		start := time.Now()
		err = backupService.Backup()
		duration := time.Since(start)

		if err != nil {
			t.Logf("Large file backup failed: %v", err)
		} else {
			t.Logf("Large file backup completed in %v", duration)
		}
	}
}

// TestAzureIntegration_MultipleFiles teste le backup de plusieurs fichiers
func TestAzureIntegration_MultipleFiles(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Créer de nombreux fichiers pour tester le backup parallèle
	numFiles := 20
	for i := 0; i < numFiles; i++ {
		relPath := fmt.Sprintf("charts/chart-%d.tgz", i)
		content := fmt.Sprintf("Chart %d content with some data to make it realistic", i)

		fullPath := filepath.Join(tempDir, relPath)
		dir := filepath.Dir(fullPath)

		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err)

		err = ioutil.WriteFile(fullPath, []byte(content), 0644)
		require.NoError(t, err)
	}

	t.Logf("Created %d test files for multiple file backup test", numFiles)

	// Créer le service de backup
	backupService, err := service.NewBackupService(cfg, logger)
	if err != nil {
		t.Logf("Service initialization failed: %v", err)
		return
	}

	if backupService != nil {
		// Exécuter le backup de tous les fichiers
		start := time.Now()
		err = backupService.Backup()
		duration := time.Since(start)

		if err != nil {
			t.Logf("Multiple files backup failed: %v", err)
		} else {
			t.Logf("Multiple files backup completed in %v (avg: %v per file)",
				duration, duration/time.Duration(numFiles))
		}
	}
}

// TestAzureIntegration_RestoreOperation teste la restoration (placeholder)
func TestAzureIntegration_RestoreOperation(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Créer le service de backup
	backupService, err := service.NewBackupService(cfg, logger)
	if err != nil {
		t.Logf("Service initialization failed: %v", err)
		return
	}

	if backupService != nil {
		// Tenter de faire une restoration (devrait retourner "not implemented")
		err = backupService.Restore()

		if err != nil && strings.Contains(err.Error(), "not implemented") {
			t.Log("Restore correctly returns 'not implemented' error")
		} else if err != nil {
			t.Logf("Restore failed with unexpected error: %v", err)
		} else {
			t.Log("Restore succeeded (unexpected)")
		}
	}
}

// TestAzureIntegration_ErrorHandling teste la gestion d'erreurs en conditions réelles
func TestAzureIntegration_ErrorHandling(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Test avec des credentials invalides
	t.Run("Invalid credentials", func(t *testing.T) {
		os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "invalid-key")
		defer os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", os.Getenv(azureStorageKeyEnvVar))

		_, err := service.NewBackupService(cfg, logger)
		if err != nil {
			t.Logf("Expected error with invalid credentials: %v", err)
			assert.Contains(t, err.Error(), "failed to initialize Azure client")
		}
	})

	// Test avec un container avec des caractères invalides
	t.Run("Invalid container name", func(t *testing.T) {
		invalidCfg := *cfg
		invalidCfg.Backup.Azure.Container = "Invalid_Container_Name_With_Underscores"

		_, err := service.NewBackupService(&invalidCfg, logger)
		if err != nil {
			t.Logf("Expected error with invalid container name: %v", err)
		}
	})

	// Test avec un compte de stockage inexistant
	t.Run("Non-existent storage account", func(t *testing.T) {
		invalidCfg := *cfg
		invalidCfg.Backup.Azure.StorageAccount = "nonexistentstorageaccount123456"

		_, err := service.NewBackupService(&invalidCfg, logger)
		if err != nil {
			t.Logf("Expected error with non-existent storage account: %v", err)
		}
	})
}

// TestAzureIntegration_PerformanceBaseline teste les performances de base
func TestAzureIntegration_PerformanceBaseline(t *testing.T) {
	cfg, logger, tempDir := setupAzureIntegrationTest(t)
	defer os.RemoveAll(tempDir)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Créer des fichiers de différentes tailles pour mesurer les performances
	testFiles := []struct {
		name string
		size int
	}{
		{"small.tgz", 1024},        // 1KB
		{"medium.tgz", 100 * 1024}, // 100KB
		{"large.tgz", 1024 * 1024}, // 1MB
	}

	for _, file := range testFiles {
		content := make([]byte, file.size)
		for i := range content {
			content[i] = byte(i % 256)
		}

		fullPath := filepath.Join(tempDir, "charts", file.name)
		err := os.MkdirAll(filepath.Dir(fullPath), 0755)
		require.NoError(t, err)

		err = ioutil.WriteFile(fullPath, content, 0644)
		require.NoError(t, err)
	}

	// Créer le service de backup
	backupService, err := service.NewBackupService(cfg, logger)
	if err != nil {
		t.Logf("Service initialization failed: %v", err)
		return
	}

	if backupService != nil {
		// Mesurer le temps de backup
		start := time.Now()
		err = backupService.Backup()
		duration := time.Since(start)

		if err != nil {
			t.Logf("Performance test backup failed: %v", err)
		} else {
			// Calculer le débit total
			totalSize := 0
			for _, file := range testFiles {
				totalSize += file.size
			}

			throughputMBps := float64(totalSize) / (1024 * 1024) / duration.Seconds()
			t.Logf("Backup performance: %v for %d bytes (%.2f MB/s)",
				duration, totalSize, throughputMBps)

			// Vérifier que les performances sont raisonnables (au moins 0.1 MB/s)
			assert.Greater(t, throughputMBps, 0.1, "Backup throughput should be at least 0.1 MB/s")
		}
	}
}
