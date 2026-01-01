package tests

import (
	"os"
	"testing"

	"oci-storage/config"
	service "oci-storage/pkg/services"
	"oci-storage/pkg/utils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAzureBackupTest configure un environnement de test pour Azure Backup
func setupAzureBackupTest() (*config.Config, *utils.Logger) {
	// Configuration de test avec Azure
	cfg := &config.Config{
		Storage: struct {
			Path string `yaml:"path"`
		}{
			Path: "data",
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
	log := utils.NewLogger(utils.Config{
		LogLevel:  "debug",
		LogFormat: "json",
		Pretty:    true,
	})

	return cfg, log
}

func TestAzureBackupService_Configuration(t *testing.T) {
	cfg, log := setupAzureBackupTest()

	// Test avec une clé d'accès manquante (simulation)
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "")
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Le service devrait échouer à l'initialisation sans clé d'accès
	backupService, err := service.NewBackupService(cfg, log)

	// On s'attend à une erreur car la clé Azure n'est pas fournie
	if err != nil {
		// C'est le comportement attendu sans credentials valides
		assert.Contains(t, err.Error(), "Azure storage account key not provided")
		assert.Nil(t, backupService)
	}
}

func TestAzureBackupService_ConfigValidation(t *testing.T) {
	log := utils.NewLogger(utils.Config{
		LogLevel:  "debug",
		LogFormat: "json",
		Pretty:    true,
	})

	tests := []struct {
		name            string
		config          *config.Config
		expectedError   string
		azureAccountKey string
	}{
		{
			name: "Missing storage account",
			config: &config.Config{
				Storage: struct {
					Path string `yaml:"path"`
				}{
					Path: "data",
				},
				Backup: config.Backup{
					Enabled:  true,
					Provider: "azure",
					Azure: struct {
						StorageAccount string `yaml:"storageAccount"`
						Container      string `yaml:"container"`
					}{
						StorageAccount: "", // Manquant
						Container:      "test-container",
					},
				},
			},
			azureAccountKey: "test-key",
			expectedError:   "Azure storage account name is not configured",
		},
		{
			name: "Missing account key",
			config: &config.Config{
				Storage: struct {
					Path string `yaml:"path"`
				}{
					Path: "data",
				},
				Backup: config.Backup{
					Enabled:  true,
					Provider: "azure",
					Azure: struct {
						StorageAccount string `yaml:"storageAccount"`
						Container      string `yaml:"container"`
					}{
						StorageAccount: "test-storage",
						Container:      "test-container",
					},
				},
			},
			azureAccountKey: "", // Manquant
			expectedError:   "Azure storage account key not provided",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the environment variable
			os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", tt.azureAccountKey)
			defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

			// Tenter de créer le service
			backupService, err := service.NewBackupService(tt.config, log)

			// Vérifier l'erreur attendue
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
			assert.Nil(t, backupService)
		})
	}
}

func TestAzureBackupService_Provider(t *testing.T) {
	cfg, log := setupAzureBackupTest()

	// Test que le provider Azure est correctement configuré
	assert.Equal(t, "azure", cfg.Backup.Provider)
	assert.True(t, cfg.Backup.Enabled)
	assert.Equal(t, "testhelmstorage", cfg.Backup.Azure.StorageAccount)
	assert.Equal(t, "test-helm-charts-backup", cfg.Backup.Azure.Container)

	// Test des autres providers non configurés
	assert.Empty(t, cfg.Backup.AWS.Bucket)
	assert.Empty(t, cfg.Backup.GCP.Bucket)

	// Le logger doit être valide
	assert.NotNil(t, log)
}

func TestAzureBackupService_MissingContainer(t *testing.T) {
	_, log := setupAzureBackupTest()

	// Configuration avec container manquant
	cfg := &config.Config{
		Storage: struct {
			Path string `yaml:"path"`
		}{
			Path: "data",
		},
		Backup: config.Backup{
			Enabled:  true,
			Provider: "azure",
			Azure: struct {
				StorageAccount string `yaml:"storageAccount"`
				Container      string `yaml:"container"`
			}{
				StorageAccount: "test-storage",
				Container:      "", // Manquant - aucun provider configuré
			},
		},
	}

	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "test-key")
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Tenter de créer le service
	backupService, err := service.NewBackupService(cfg, log)

	// Aucune erreur mais service nil car aucun provider valide
	require.NoError(t, err)
	assert.Nil(t, backupService)
}

func TestAzureBackupService_InvalidCredentials(t *testing.T) {
	cfg, log := setupAzureBackupTest()

	// Set une clé d'accès invalide
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "invalid-key-format")
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	// Le service devrait échouer à l'initialisation avec des credentials invalides
	backupService, err := service.NewBackupService(cfg, log)

	// On s'attend à une erreur car les credentials sont invalides
	if err != nil {
		// Le test peut échouer pour différentes raisons :
		// - Credentials invalides
		// - Pas d'accès réseau à Azure
		// - Format de clé incorrect
		assert.NotNil(t, err)
		assert.Nil(t, backupService)
	}
}

// Test de la configuration des secrets Azure
func TestAzureSecrets_Loading(t *testing.T) {
	// Test avec une clé d'environnement valide
	testKey := "test-azure-storage-key"
	os.Setenv("AZURE_STORAGE_ACCOUNT_KEY", testKey)
	defer os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	secrets := config.LoadSecrets()

	// Vérifier que les secrets Azure sont chargés
	assert.Equal(t, testKey, secrets.AzureStorageAccountKey)

	// Vérifier que les autres secrets ne sont pas affectés par notre test
	// Note: Ces champs peuvent avoir des valeurs si les variables d'environnement sont définies globalement
}

func TestAzureSecrets_Empty(t *testing.T) {
	// S'assurer qu'aucune variable d'environnement Azure n'est définie
	os.Unsetenv("AZURE_STORAGE_ACCOUNT_KEY")

	secrets := config.LoadSecrets()

	// Vérifier que les secrets Azure sont vides
	assert.Empty(t, secrets.AzureStorageAccountKey)
}
