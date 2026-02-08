# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Strict Rules

- **NEVER run `helm upgrade`, `helm install`, or `task helm-install`** - User deploys manually
- **NEVER run `task build`** - User builds manually
- **NEVER run `git commit` or `git push`** unless explicitly asked
- **Focus on fixing code only** - Do not deploy, build, or commit without explicit request

## Project Overview

OCI-compatible registry for storing Helm charts and container images, with a pull-through proxy/cache for upstream registries. Built in Go 1.24 with the Fiber v2 web framework. Deployed to Kubernetes via ArgoCD (commit to main triggers auto-sync).

## Development Commands

```bash
task run-dev          # Start dev server with Air hot reload (runs from src/)
task test-unit        # Run unit tests: go test ./pkg/... -v (from src/)
task test-e2e         # Run E2E tests via tests/test-e2e.sh
task helm-template    # Render Helm templates to helm/rendered.yaml
task start / stop     # Docker compose up/down
```

Run a single test file or function directly:
```bash
cd src && go test ./pkg/handlers/ -run TestSpecificFunction -v
```

The `tests/` directory is a **separate Go module** (`oci-storage-tests`) with `replace oci-storage => ../src`. Run integration tests from there:
```bash
cd tests && go test -v -run TestAuth ./...
```

CI uses `golangci-lint v1.64.8` for linting and runs from the `src/` working directory.

## Architecture

### Module Structure

Two Go modules exist:
- `src/` - Main application module (`module oci-storage`)
- `tests/` - Integration test module (`module oci-storage-tests`, replaces `oci-storage => ../src`)

### Dependency Injection & Service Wiring (`src/cmd/server/main.go`)

Services are created in `setupServices()` with a circular dependency pattern: `ChartService` needs `IndexService` and vice versa. Resolution: create a temporary `ChartService` with nil `IndexService`, create `IndexService` with that, then create the final `ChartService` with the real `IndexService`.

Optional services (`ProxyService`, `ScanService`) are only instantiated when enabled in config.

### Interfaces (`src/pkg/interfaces/interfaces.go`)

All services have interfaces: `ChartServiceInterface`, `ImageServiceInterface`, `IndexServiceInterface`, `ProxyServiceInterface`, `ScanServiceInterface`. Handlers accept interfaces, mocks implement them via testify (`src/pkg/handlers/mocks.go`).

### Handler/Route Organization

Routes are defined directly in `main.go`. Key route groups:
- `/` - Web UI (Fiber HTML templates from `views/`)
- `/chart/*`, `/charts`, `/index.yaml` - Helm repository API
- `/image/*` - Docker image browsing (wildcard routing for nested proxy paths)
- `/v2/*` - OCI registry protocol (auth middleware applied to this group only)
- `/cache/*`, `/gc/*` - Proxy cache management
- `/api/scan/*` - Trivy vulnerability scanning & security gate
- `/backup`, `/restore` - Cloud backup operations

OCI routes have multiple nesting levels (1-5 path segments) to support `proxy/registry/namespace/image` patterns. These are separate handler methods (e.g., `HandleManifest`, `HandleManifestNested`, `HandleManifestDeepNested`, etc.).

### Authentication

Basic auth middleware (`src/pkg/middlewares/auth.go`) applies only to `/v2/*` routes. GET/HEAD requests allow anonymous access; write operations (PUT/POST/DELETE/PATCH) require credentials. Auth users loaded from: `HELM_USERS` env var > `HELM_USER_N_*` prefixed env vars > `config/auth.yaml` file.

### PathManager (`src/pkg/utils/paths.go`)

Central utility managing storage directory structure under `data/`: `temp/`, `blobs/`, `manifests/`, `charts/`, `images/`, `cache/metadata/`. All services use PathManager for file path resolution.

### Configuration

- Main config: `src/config/config.yaml` (loaded at startup, most values overridable via env vars)
- Auth config: `src/config/auth.yaml` (separate file, path via `AUTH_FILE` env var)
- Config struct: `src/config/config.go` - includes `Server`, `Storage`, `Logging`, `Auth`, `Backup`, `Proxy`, `Trivy` sections
- Backup supports AWS S3, GCP Cloud Storage, and Azure Blob Storage

### Version Info

Build-time injection via ldflags into `src/pkg/version/version.go`. The Dockerfile and CI pass `VERSION`, `COMMIT`, `BUILD_TIME` args.

### Key Technical Details

- Port 3030 is hardcoded in `main.go`
- Body limit is 1GB (for large Docker image layers)
- Proxy has concurrency limiting (3 parallel blob downloads) and dynamic timeouts based on blob size
- Logging uses logrus with a custom `Logger` wrapper (`src/pkg/utils/logger.go`)
- Code comments are a mix of French and English
