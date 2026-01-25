// pkg/models/image.go
package models

import "time"

// ImageMetadata represents Docker image metadata
type ImageMetadata struct {
	Name       string         `json:"name"`
	Repository string         `json:"repository"`
	Tag        string         `json:"tag"`
	Digest     string         `json:"digest"`
	Size       int64          `json:"size"`
	Created    time.Time      `json:"created"`
	Config     *ImageConfig   `json:"config,omitempty"`
	Layers     []LayerInfo    `json:"layers,omitempty"`
	Platforms  []PlatformInfo `json:"platforms,omitempty"` // Available platforms for multi-arch images
}

// PlatformInfo represents a platform in a multi-arch image
type PlatformInfo struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
	Digest       string `json:"digest,omitempty"`
}

// ImageConfig represents the configuration of a Docker image
type ImageConfig struct {
	Architecture string           `json:"architecture"`
	OS           string           `json:"os"`
	Author       string           `json:"author,omitempty"`
	Created      string           `json:"created,omitempty"`
	Config       *ContainerConfig `json:"config,omitempty"`
	History      []HistoryEntry   `json:"history,omitempty"`
	RootFS       *RootFS          `json:"rootfs,omitempty"`
}

// ContainerConfig represents the container configuration
type ContainerConfig struct {
	Hostname     string              `json:"Hostname,omitempty"`
	Domainname   string              `json:"Domainname,omitempty"`
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	Volumes      map[string]struct{} `json:"Volumes,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
}

// HistoryEntry represents a layer history entry
type HistoryEntry struct {
	Created    string `json:"created,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	EmptyLayer bool   `json:"empty_layer,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// RootFS represents the root filesystem
type RootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

// LayerInfo represents information about a layer
type LayerInfo struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ImageGroup groups images by repository name
type ImageGroup struct {
	Name string          `json:"name"` // Repository name
	Tags []ImageMetadata `json:"tags"` // List of available tags
}

// GroupImagesByName groups images by their repository name
func GroupImagesByName(images []ImageMetadata) []ImageGroup {
	imageGroups := make(map[string][]ImageMetadata)

	for _, image := range images {
		imageGroups[image.Name] = append(imageGroups[image.Name], image)
	}

	result := make([]ImageGroup, 0, len(imageGroups))
	for name, tags := range imageGroups {
		result = append(result, ImageGroup{
			Name: name,
			Tags: tags,
		})
	}

	return result
}

// OCI Media Types for Docker images
const (
	// Docker manifest types
	MediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerConfig       = "application/vnd.docker.container.image.v1+json"
	MediaTypeDockerLayer        = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	MediaTypeDockerLayerNonDist = "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"

	// OCI manifest types
	MediaTypeOCIManifest     = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIManifestList = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIConfig       = "application/vnd.oci.image.config.v1+json"
	MediaTypeOCILayer        = "application/vnd.oci.image.layer.v1.tar+gzip"
	MediaTypeOCILayerNonDist = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"

	// Helm chart types
	MediaTypeHelmConfig = "application/vnd.cncf.helm.config.v1+json"
	MediaTypeHelmChart  = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
)

// ArtifactType represents the type of OCI artifact
type ArtifactType string

const (
	ArtifactTypeHelmChart   ArtifactType = "helm"
	ArtifactTypeDockerImage ArtifactType = "docker"
	ArtifactTypeUnknown     ArtifactType = "unknown"
)

// DetectArtifactType determines if an OCI manifest is a Helm chart or Docker image
func DetectArtifactType(manifest *OCIManifest) ArtifactType {
	if manifest == nil {
		return ArtifactTypeUnknown
	}

	// Check config media type
	switch manifest.Config.MediaType {
	case MediaTypeHelmConfig:
		return ArtifactTypeHelmChart
	case MediaTypeDockerConfig, MediaTypeOCIConfig:
		return ArtifactTypeDockerImage
	}

	// Check layers for Helm chart content
	for _, layer := range manifest.Layers {
		if layer.MediaType == MediaTypeHelmChart {
			return ArtifactTypeHelmChart
		}
		if layer.MediaType == MediaTypeDockerLayer ||
			layer.MediaType == MediaTypeOCILayer ||
			layer.MediaType == MediaTypeDockerLayerNonDist ||
			layer.MediaType == MediaTypeOCILayerNonDist {
			return ArtifactTypeDockerImage
		}
	}

	return ArtifactTypeUnknown
}
