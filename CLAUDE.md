# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## ⛔ STRICT RULES - DO NOT VIOLATE

- **NEVER run `helm upgrade`, `helm install`, or `task helm-install`** - User deploys manually
- **NEVER run `task build`** - User builds manually
- **NEVER run `git commit` or `git push`** unless explicitly asked
- **Focus on fixing code only** - Do not deploy, build, or commit without explicit request

## Project Overview

oci storage is a lightweight OCI-compatible registry for storing and managing Helm charts. It's built in Go using the Fiber web framework and provides both a web interface and REST API for chart management.

## Development Commands

Use the Taskfile for common development tasks:

- `task run-dev` - Start development server with hot reload using Air
- `task build` - Build and push Docker image  
- `task start` - Start Docker container via docker-compose
- `task stop` - Stop Docker container
- `task helm-install` - Deploy to Kubernetes cluster (local testing only)
- `task helm-template` - Generate Helm templates for debugging

**IMPORTANT**: Do NOT deploy oci-storage with `task helm-install` in production. ArgoCD manages the deployment - just commit and push changes, ArgoCD will sync automatically.

For testing Helm functionality:
- `task test-upload-chart` - Test chart upload via HTTP
- `task test-push-chart` - Test chart push via OCI protocol
- `task test-pull-chart` - Test chart pull via OCI protocol

## Architecture

The application follows a layered architecture:

**Main Entry Point**: `src/cmd/server/main.go` - Sets up services, handlers, and HTTP routes

**Core Services** (in `src/pkg/services/`):
- `ChartService` - Manages chart storage and retrieval
- `IndexService` - Maintains Helm repository index.yaml
- `BackupService` - Handles cloud backup/restore operations

**Handlers** (in `src/pkg/handlers/`):
- `HelmHandler` - Traditional Helm repository API (/chart, /index.yaml)
- `OCIHandler` - OCI registry protocol implementation (/v2/*)
- `BackupHandler` - Backup/restore endpoints
- `ConfigHandler` - Configuration endpoints

**Key Integrations**:
- Services are injected with circular dependency resolution (ChartService ↔ IndexService)
- Authentication middleware applies only to OCI routes (/v2/*)
- Static files served from `views/static/`
- Templates in `views/` directory using Fiber's HTML template engine

## Configuration

Main config file: `src/config/config.yaml`
Auth config file: `src/config/auth.yaml` (loaded separately)

Configuration supports environment variable overrides for server port and logging level.

## Testing

Go tests use testify framework. Mock implementations are in `src/pkg/handlers/mocks.go`.

Run tests with standard Go commands:
- `task test` - Run all Go tests
- `task test-unit` - Run unit tests only (pkg/*)
- `task test-auth` - Run authentication tests
- `task test-proxy` - Run Docker proxy integration tests (requires running server)
- `task test-proxy-ci` - Run proxy tests with configurable URL for CI

For proxy tests, you can configure:
- `PORTAL_URL` - Portal URL (default: http://localhost:3030)
- `PORTAL_AUTH` - Auth credentials (default: admin:admin123)

## Key Technical Details

- Uses Go modules with `oci-storage` as module name
- Port 3030 is hardcoded in main.go and used throughout
- Chart storage path configurable via config.yaml
- Supports both AWS S3 and GCP Cloud Storage for backups
- OCI protocol implementation handles manifest and blob operations
- Web interface provides chart browsing and download functionality