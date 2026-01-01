# Tests Azure Blob Storage

Ce document décrit les tests pour l'implémentation Azure Blob Storage backup dans oci storage.

## Types de Tests

### 1. Tests Unitaires (`azure_backup_test.go`)

Tests de base pour la configuration et la validation :

- **Configuration validation** - Teste les différentes configurations Azure
- **Credentials loading** - Teste le chargement des secrets depuis les variables d'environnement
- **Error handling** - Teste la gestion d'erreurs de base

```bash
# Exécuter les tests unitaires Azure
task test-azure
```

### 2. Tests Complets (`azure_backup_complete_test.go`)

Suite de tests complète utilisant testify/suite :

- **Initialization tests** - Tests d'initialisation du service
- **Configuration validation** - Validation approfondie des configurations
- **Provider selection** - Tests de sélection du bon provider
- **PathManager integration** - Tests d'intégration avec PathManager
- **Error handling** - Gestion d'erreurs avancée
- **Concurrency tests** - Tests de sécurité en concurrence

### 3. Tests Mock (`azure_backup_mock_test.go`)

Tests utilisant des mocks pour éviter les dépendances externes :

- **Backup operations** - Tests des opérations de backup avec mocks
- **Restore operations** - Tests des opérations de restore avec mocks
- **File operations** - Tests des opérations sur fichiers
- **Large file handling** - Tests de gestion des gros fichiers
- **Error scenarios** - Tests de différents scénarios d'erreur

```bash
# Exécuter les tests mock Azure
task test-azure-mock
```

### 4. Tests d'Intégration (`azure_integration_test.go`)

Tests avec de vraies connexions Azure (nécessitent des credentials) :

- **Basic backup** - Test de backup de base avec Azure réel
- **Container creation** - Test de création automatique de container
- **Large file backup** - Test de backup de gros fichiers
- **Multiple files backup** - Test de backup de nombreux fichiers
- **Performance baseline** - Tests de performance de base

## Configuration des Tests d'Intégration

Pour exécuter les tests d'intégration, vous devez :

### 1. Variables d'Environnement Requises

```bash
export AZURE_INTEGRATION_TEST=true
export AZURE_TEST_STORAGE_ACCOUNT="your-storage-account"
export AZURE_TEST_STORAGE_KEY="your-storage-account-key"
export AZURE_TEST_CONTAINER="test-container-prefix"
```

### 2. Exécution

```bash
# Exécuter les tests d'intégration Azure
task test-azure-integration
```

⚠️ **Attention** : Les tests d'intégration :
- Créent de vrais containers Azure
- Uploadent de vrais fichiers
- Peuvent générer des coûts Azure
- Nécessitent une connexion Internet

## Structure des Tests

### Fichiers de Test

- `azure_backup_test.go` - Tests unitaires de base
- `azure_backup_complete_test.go` - Suite de tests complète
- `azure_backup_mock_test.go` - Tests avec mocks
- `azure_integration_test.go` - Tests d'intégration avec Azure réel

### Données de Test

Les tests utilisent des fichiers de test simulant un environnement oci storage :

```
testdata/
├── charts/
│   ├── my-chart-1.0.0.tgz
│   ├── my-chart-1.0.1.tgz
│   └── large-chart.tgz
├── manifests/
│   └── my-chart/
│       └── 1.0.0.json
├── blobs/
│   └── sha256abc123
└── index.yaml
```

## Commandes de Test

### Tests Rapides (Sans Connexion Azure)

```bash
# Tous les tests externes (incluant Azure)
task test-external

# Tests Azure uniquement (unitaires)
task test-azure

# Tests Azure avec mocks
task test-azure-mock
```

### Tests Complets (Avec Connexion Azure)

```bash
# Tests d'intégration Azure (nécessite credentials)
task test-azure-integration
```

### Tests Spécifiques

```bash
# Test spécifique par nom
cd tests && go test -run TestAzureBackupService_Configuration -v

# Tests avec timeout
cd tests && go test -timeout 30s -run TestAzure -v

# Tests avec sortie détaillée
cd tests && go test -v -count=1 -run TestAzure
```

## Couverture de Test

### Fonctionnalités Testées ✅

- ✅ Configuration Azure (storage account, container, credentials)
- ✅ Validation des paramètres
- ✅ Chargement des secrets depuis les variables d'environnement
- ✅ Gestion d'erreurs (credentials invalides, configurations manquantes)
- ✅ Intégration avec PathManager
- ✅ Sécurité en concurrence
- ✅ Opérations de backup (mocked)
- ✅ Gestion des gros fichiers
- ✅ Tests de performance de base

### Fonctionnalités Non Testées ❌

- ❌ Restoration réelle (fonctionnalité non implémentée)
- ❌ Gestion des permissions Azure détaillées
- ❌ Tests de résilience réseau avancés
- ❌ Tests de stress avec énormément de fichiers

## Debugging des Tests

### Logs Détaillés

```bash
# Activer les logs debug
cd tests && AZURE_INTEGRATION_TEST=true go test -run TestAzureIntegration -v -args -test.v=true
```

### Variables d'Environnement de Debug

```bash
# Logs Azure SDK (si supporté)
export AZURE_STORAGE_ACCOUNT_DEBUG=true

# Timeout étendu pour les tests lents
export AZURE_TEST_TIMEOUT=300s
```

## Sécurité

### Variables d'Environnement Sensibles

⚠️ **Ne jamais committer** les variables suivantes :
- `AZURE_TEST_STORAGE_KEY`
- `AZURE_STORAGE_ACCOUNT_KEY`

### Isolation des Tests

- Chaque test d'intégration utilise un container unique
- Les containers de test sont préfixés par timestamp
- Les répertoires temporaires sont automatiquement nettoyés

## Contribution

### Ajout de Nouveaux Tests

1. **Tests unitaires** → `azure_backup_test.go`
2. **Tests avec mocks** → `azure_backup_mock_test.go`
3. **Tests d'intégration** → `azure_integration_test.go`
4. **Suites de tests complexes** → `azure_backup_complete_test.go`

### Conventions

- Préfixer les tests par `TestAzure`
- Utiliser `testify/assert` et `testify/require`
- Nettoyer les ressources dans `defer`
- Documenter les tests complexes
- Utiliser des noms de tests descriptifs