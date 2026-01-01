# oci storage

A lightweight and standalone OCI (Open Container Initiative) registry for storing, managing, and sharing your Helm charts.

## 📋 Description

oci storage is a simple yet powerful solution that allows you to host your own Helm charts in an OCI-compatible registry. This project implements the OCI specifications to enable efficient storage and distribution of Helm charts without depending on external services.

## ✨ Features

- 📦 Complete OCI registry for Helm charts
- 🔄 Version and tag management
- 🔒 Simple and secure authentication
- 🌐 REST API for programmatic interaction
- 📊 Web interface for chart management and visualization
- 🔍 Search and filtering of available charts
- 💾 Backup to AWS / GCP buckets
- 🔄 Simple backup with a dedicated button

## 🛠️ Prerequisites

- Kubernetes 1.18+
- Helm 3.8.0+ (OCI support)
- Docker (for building the image if necessary)

## 🚀 Installation

### Chart Preparation

Before installing or packaging the chart, run our script to copy the configuration file:

```bash
# Make the script executable
chmod +x scripts/copy-config.sh

# Run the script
./scripts/copy-config.sh
```

### Installation with our script (recommended)

```bash
# Make the script executable
chmod +x update-helm-chart.sh

# Install or update the chart (with default namespace)
./update-helm-chart.sh

# Or specify a namespace and release name
./update-helm-chart.sh my-namespace my-oci-storage
```

### Manual installation with Helm

```bash
# Install the chart
helm install oci-storage ./helm

# Or with a specific namespace
helm install oci-storage ./helm --namespace my-namespace --create-namespace
```

### Using the OCI registry

```bash
# Package your chart
helm package <yourchart>

# Login to the OCI registry
helm registry login localhost:3030 \
  --username admin \
  --password admin123

# Push the chart to the OCI registry
helm push ./your-chart-1.0.0.tgz oci://localhost:3030
```

## 📝 Configuration

The Helm chart uses a `config.yaml` file for its main configuration, which is automatically integrated into a ConfigMap during installation.

### ConfigMap Structure

The `src/config/config.yaml` file is copied into the Helm chart and used as the basis for the ConfigMap. Values can be overridden by those specified in `values.yaml`.

### Main configuration options

```yaml
# config.yaml
server:
  port: 3030
 
auth:
 
  users:
  - username: "admin"
    password: "admin123"

logging:
  level: "info"
  format: "text" # or "json"

# Optional backup configuration
backup:
  enabled: false
  gcp:

    bucket: "oci-storage-backup"
    projectID: "your-project"
  # aws:
  #   bucket: "oci-storage-backup"
  #   region: "eu-west-1"
```

## 🧩 Usage

### Web Interface

![alt text](assets/home.png)

![alt text](assets/detail.png)
The web interface is accessible at the service address (default `http://localhost:3030`) and allows:

- View all available charts
- Download charts directly from the interface
- View details and values of each chart
- Perform backups via the dedicated button

### REST API

```bash
# List all charts
curl -X GET http://localhost:3030/api/charts

# Get details of a specific chart
curl -X GET http://localhost:3030/api/charts/chart-name/version
```

### Deployment

```bash
# Deploy the application
helm install oci-storage ./helm
```

### Helm Commands

```bash
# List available charts in the registry
helm search repo oci-storage

# Install a chart from the registry
helm install my-app oci://localhost:3030/chart-name --version 1.0.0
 
# connect to the registry
helm registry login localhost:3031 \
  --username admin \
  --password admin123 \
```

## 🤝 Contribution

Contributions are welcome! Feel free to open an issue or a pull request.

## 📄 License

This project is under MIT license.