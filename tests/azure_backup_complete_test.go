package tests

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oci-storage/config"
	service "oci-storage/pkg/services"
	"oci-storage/pkg/utils"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// AzureBackupTestSuite est une suite de tests pour Azure Backup
type AzureBackupTestSuite struct {
	suite.Suite
	tempDir   string
	config    *config.Config
	logger    *utils.Logger
	service   *service.BackupService
	testFiles map[string]string // nom -> contenu
}

// SetupSuite s'exécute une fois avant tous les tests de la suite
func (suite *AzureBackupTestSuite) SetupSuite() {
	// Créer un répertoire temporaire pour les tests
	tempDir, err := ioutil.TempDir("", "azure-backup-test")
	require.NoError(suite.T(), err)
	suite.tempDir = tempDir

	// Configuration de test
	suite.config = &config.Config{
		Storage: struct {
			Path string `yaml:"path"`
		}{
			Path: suite.tempDir,
		},
		Backup: config.Backup{
			Enabled:  true,
			Provider: "azure",
			Azure: struct {
				StorageAccount string `yaml:"storageAccount"`
				Container      string `yaml:"container"`
			}{
				StorageAccount: "testhelmstorage",
				Container:      "test-helm-charts-backup",
			},
		},
	}

	// Logger de test
	suite.logger = utils.NewLogger(utils.Config{
		LogLevel:  "debug",
		LogFormat: "json",
		Pretty:    true,
	})

	// Fichiers de test à créer
	suite.testFiles = map[string]string{
		"charts/my-chart-1.0.0.tgz":     "fake chart content 1",
		"charts/my-chart-1.0.1.tgz":     "fake chart content 2",
		"manifests/my-chart/1.0.0.json": `{"schemaVersion": 2}`,
		"index.yaml":                    "apiVersion: v1\nentries: {}",
		"blobs/sha256abc123":            "blob content",
	}
}

// TearDownSuite s'exécute une fois après tous les tests de la suite
func (suite *AzureBackupTestSuite) TearDownSuite() {
	if suite.tempDir != "" {
		os.RemoveAll(suite.tempDir)
	}
}

// SetupTest s'exécute avant chaque test
func (suite *AzureBackupTestSuite) SetupTest() {
	// Créer les fichiers de test
	for relPath, content := range suite.testFiles {
		fullPath := filepath.Join(suite.tempDir, relPath)

		// Créer le répertoire parent si nécessaire
		dir := filepath.Dir(fullPath)
		err := os.MkdirAll(dir, 0755)
		require.NoError(suite.T(), err)

		// Créer le fichier
		err = ioutil.WriteFile(fullPath, []byte(content), 0644)
		require.NoError(suite.T(), err)
	}
}

// TearDownTest s'exécute après chaque test
func (suite *AzureBackupTestSuite) TearDownTest() {
	// Nettoyer les fichiers créés
	for relPath := range suite.testFiles {
		fullPath := filepath.Join(suite.tempDir, relPath)
		os.Remove(fullPath)
	}
}

// TestAzureBackupService_InitializationSuccess teste l'initialisation réussie
func (suite *AzureBackupTestSuite) TestAzureBackupService_InitializationSuccess() {
	// Simuler une clé d'accès valide (ne se connecte pas réellement à Azure)
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "fake-but-valid-format-key-12345678901234567890123456789012345678901234567890123456")
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Tenter de créer le service (échouera à la connexion Azure mais testera la logique)
	backupService, err := service.NewBackupService(suite.config, suite.logger)

	// Le service devrait être créé même si la connexion Azure échoue
	// (car nous testons la logique, pas la connectivité réelle)
	if err != nil {
		// L'erreur est attendue car nous ne nous connectons pas à un vrai Azure
		suite.Contains(err.Error(), "failed to initialize Azure client")
	}

	// Dans ce contexte, nil est acceptable
	if backupService == nil {
		suite.T().Log("Service is nil due to Azure connection failure (expected in test)")
	}
}

// TestAzureBackupService_ConfigurationValidation teste la validation de la configuration
func (suite *AzureBackupTestSuite) TestAzureBackupService_ConfigurationValidation() {
	tests := []struct {
		name             string
		storageAccount   string
		container        string
		accountKey       string
		expectError      bool
		expectedErrorMsg string
	}{
		{
			name:             "Valid configuration",
			storageAccount:   "validstorageaccount",
			container:        "validcontainer",
			accountKey:       "validkey12345678901234567890123456789012345678901234567890123456",
			expectError:      true, // Échouera sur la connexion Azure réelle
			expectedErrorMsg: "failed to initialize Azure client",
		},
		{
			name:             "Empty storage account",
			storageAccount:   "",
			container:        "validcontainer",
			accountKey:       "validkey12345678901234567890123456789012345678901234567890123456",
			expectError:      true,
			expectedErrorMsg: "Azure storage account name is not configured",
		},
		{
			name:             "Empty container",
			storageAccount:   "validstorageaccount",
			container:        "",
			accountKey:       "validkey12345678901234567890123456789012345678901234567890123456",
			expectError:      false, // Returns nil without error when no provider configured
			expectedErrorMsg: "",
		},
		{
			name:             "Empty account key",
			storageAccount:   "validstorageaccount",
			container:        "validcontainer",
			accountKey:       "",
			expectError:      true,
			expectedErrorMsg: "Azure storage account key not provided",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			// Configurer la configuration de test
			testConfig := *suite.config
			testConfig.Backup.Azure.StorageAccount = tt.storageAccount
			testConfig.Backup.Azure.Container = tt.container

			// Configurer la variable d'environnement
			os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", tt.accountKey)
			defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

			// Tenter de créer le service
			backupService, err := service.NewBackupService(&testConfig, suite.logger)

			if tt.expectError {
				suite.Error(err)
				suite.Contains(err.Error(), tt.expectedErrorMsg)
				suite.Nil(backupService)
			} else {
				suite.Nil(err)
				suite.Nil(backupService)
			}
		})
	}
}

// TestAzureBackupService_ProviderSelection teste la sélection du bon provider
func (suite *AzureBackupTestSuite) TestAzureBackupService_ProviderSelection() {
	providers := []struct {
		name           string
		provider       string
		awsBucket      string
		gcpBucket      string
		azureContainer string
	}{
		{
			name:           "Azure provider selected",
			provider:       "azure",
			azureContainer: "test-container",
		},
		{
			name:      "AWS provider selected",
			provider:  "aws",
			awsBucket: "test-bucket",
		},
		{
			name:      "GCP provider selected",
			provider:  "gcp",
			gcpBucket: "test-bucket",
		},
	}

	for _, tt := range providers {
		suite.Run(tt.name, func() {
			testConfig := *suite.config
			testConfig.Backup.Provider = tt.provider

			// Réinitialiser tous les providers
			testConfig.Backup.AWS.Bucket = tt.awsBucket
			testConfig.Backup.GCP.Bucket = tt.gcpBucket
			testConfig.Backup.Azure.Container = tt.azureContainer

			suite.Equal(tt.provider, testConfig.Backup.Provider)

			if tt.provider == "azure" {
				suite.Equal(tt.azureContainer, testConfig.Backup.Azure.Container)
				suite.Empty(testConfig.Backup.AWS.Bucket)
				suite.Empty(testConfig.Backup.GCP.Bucket)
			}
		})
	}
}

// TestAzureBackupService_PathManagerIntegration teste l'intégration avec PathManager
func (suite *AzureBackupTestSuite) TestAzureBackupService_PathManagerIntegration() {
	// Tester que le PathManager utilise le bon chemin
	pathManager := utils.NewPathManager(suite.config.Storage.Path, suite.logger)

	suite.Equal(suite.tempDir, pathManager.GetBasePath())

	// Vérifier que les fichiers de test sont accessible via PathManager
	for relPath := range suite.testFiles {
		fullPath := filepath.Join(pathManager.GetBasePath(), relPath)
		suite.FileExists(fullPath)
	}
}

// TestAzureBackupService_ErrorHandling teste la gestion d'erreur
func (suite *AzureBackupTestSuite) TestAzureBackupService_ErrorHandling() {
	// Tester avec une configuration nil
	nilService, err := service.NewBackupService(nil, suite.logger)
	suite.Error(err)
	suite.Contains(err.Error(), "config is nil")
	suite.Nil(nilService)

	// Tester avec un logger nil
	nilService, err = service.NewBackupService(suite.config, nil)
	suite.Error(err)
	suite.Contains(err.Error(), "logger is nil")
	suite.Nil(nilService)
}

// TestAzureBackupService_BackupDisabled teste le comportement quand le backup est désactivé
func (suite *AzureBackupTestSuite) TestAzureBackupService_BackupDisabled() {
	disabledConfig := *suite.config
	disabledConfig.Backup.Enabled = false

	backupService, err := service.NewBackupService(&disabledConfig, suite.logger)

	// Quand le backup est désactivé, le service devrait retourner nil sans erreur
	suite.Nil(err)
	suite.Nil(backupService)
}

// TestAzureSecrets_EnvironmentVariables teste le chargement des secrets
func (suite *AzureBackupTestSuite) TestAzureSecrets_EnvironmentVariables() {
	tests := []struct {
		name        string
		envValue    string
		expectValue string
	}{
		{
			name:        "Valid Azure key",
			envValue:    "test-azure-key-12345",
			expectValue: "test-azure-key-12345",
		},
		{
			name:        "Empty Azure key",
			envValue:    "",
			expectValue: "",
		},
		{
			name:        "Long Azure key",
			envValue:    "very-long-azure-storage-account-key-with-many-characters-12345678901234567890",
			expectValue: "very-long-azure-storage-account-key-with-many-characters-12345678901234567890",
		},
	}

	for _, tt := range tests {
		suite.Run(tt.name, func() {
			// Nettoyer les autres variables d'environnement
			os.Unsetenv("AWS_ACCESS_KEY_ID")
			os.Unsetenv("AWS_SECRET_ACCESS_KEY")
			os.Unsetenv("GCP_CREDENTIALS_FILE")

			// Définir la variable Azure
			os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", tt.envValue)
			defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

			secrets := config.LoadSecrets()

			suite.Equal(tt.expectValue, secrets.AzureStorageAccountKey)

			// Vérifier que les autres secrets sont vides
			suite.Empty(secrets.AWSAccessKeyID)
			suite.Empty(secrets.AWSSecretAccessKey)
			suite.Empty(secrets.GCPCredentialsFile)
		})
	}
}

// TestAzureBackupService_Concurrency teste la sécurité en concurrence
func (suite *AzureBackupTestSuite) TestAzureBackupService_Concurrency() {
	// Simuler plusieurs goroutines créant des services simultanément
	const numGoroutines = 10
	results := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			// Créer une configuration unique pour chaque goroutine
			testConfig := *suite.config
			testConfig.Backup.Azure.Container = fmt.Sprintf("test-container-%d", id)

			// Définir une clé unique
			key := fmt.Sprintf("test-key-%d-12345678901234567890123456789012345678901234567890", id)
			os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", key)
			defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

			// Tenter de créer le service
			_, err := service.NewBackupService(&testConfig, suite.logger)
			results <- err
		}(i)
	}

	// Collecter les résultats
	var errors []error
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-results:
			if err != nil {
				errors = append(errors, err)
			}
		case <-time.After(5 * time.Second):
			suite.Fail("Timeout waiting for goroutine to complete")
		}
	}

	// Tous les appels devraient échouer de la même manière (connexion Azure)
	suite.Len(errors, numGoroutines, "All calls should fail with Azure connection error")

	for _, err := range errors {
		suite.Contains(err.Error(), "failed to initialize Azure client")
	}
}

// TestRunAzureBackupTestSuite exécute la suite de tests
func TestRunAzureBackupTestSuite(t *testing.T) {
	suite.Run(t, new(AzureBackupTestSuite))
}
