# Tests Externes oci storage

Ce répertoire contient les tests unitaires et d'intégration externes pour oci storage, organisés séparément du code source principal.

## Structure

```
tests/
├── README.md              # Ce fichier
├── go.mod                 # Module Go séparé
├── go.sum                 # Dépendances
└── auth_test.go           # Tests d'authentification
```

## Tests d'Authentification

Le fichier `auth_test.go` contient des tests complets du middleware d'authentification :

### Tests Couverts

1. **TestAuthMiddleware_ValidCredentials**
   - Test des credentials valides pour tous les utilisateurs configurés
   - Vérification de l'authentification réussie

2. **TestAuthMiddleware_InvalidCredentials**
   - Test des mots de passe incorrects
   - Test des utilisateurs inexistants
   - Test des credentials vides

3. **TestAuthMiddleware_MissingAuthHeader**
   - Test de l'absence d'header d'authentification
   - Vérification du header `WWW-Authenticate`
   - Vérification de la réponse JSON d'erreur

4. **TestAuthMiddleware_InvalidAuthFormat**
   - Test des formats d'authentification invalides
   - Test du base64 malformé
   - Test des séparateurs manquants ou multiples

5. **TestAuthMiddleware_CaseSensitivity**
   - Vérification de la sensibilité à la casse
   - Test username et password en majuscules

6. **TestAuthMiddleware_MultipleUsers**
   - Test que tous les utilisateurs configurés peuvent s'authentifier
   - Vérification de l'isolation entre utilisateurs

## Exécution des Tests

### Via Taskfile (Recommandé)

```bash
# Tests d'authentification seulement
task test-auth

# Tous les tests externes
task test-external
```

### Directement avec Go

```bash
# Depuis le répertoire tests/
cd tests
go mod tidy
go test -v

# Test spécifique
go test -run TestAuthMiddleware_ValidCredentials -v
```

## Configuration

Les tests utilisent une configuration de test avec des utilisateurs prédéfinis :

```yaml
auth:
  users:
    - username: "admin"
      password: "admin123"
    - username: "user" 
      password: "password"
    - username: "test"
      password: "test123"
```

## Ajout de Nouveaux Tests

1. Créer de nouveaux fichiers `*_test.go` dans ce répertoire
2. Utiliser le package `tests`
3. Importer le module principal : `oci-storage`
4. Suivre les conventions de test Go
5. Ajouter les commandes au `taskfile.yaml` si nécessaire

## Dépendances

- `github.com/gofiber/fiber/v2` - Framework web
- `github.com/stretchr/testify` - Assertions et mocks
- `oci-storage` - Module principal (via replace directive)

## Module Séparé

Ce répertoire utilise son propre `go.mod` pour :
- Isolation des dépendances de test
- Flexibilité dans les versions des dépendances
- Tests indépendants du module principal
- Éviter la pollution du module principal avec des dépendances de test