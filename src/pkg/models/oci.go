package models

// OCIDescriptor represents an OCI content descriptor
type OCIDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	URLs        []string          `json:"urls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Platform    *OCIPlatform      `json:"platform,omitempty"`
}

// OCIPlatform describes the platform which the image runs on
type OCIPlatform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os.version,omitempty"`
	OSFeatures   []string `json:"os.features,omitempty"`
	Variant      string   `json:"variant,omitempty"`
}

// OCIManifest represents an OCI image manifest
type OCIManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Config        OCIDescriptor     `json:"config"`
	Layers        []OCIDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// OCIIndex represents an OCI image index (manifest list)
type OCIIndex struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	Manifests     []OCIDescriptor   `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

// GetTotalSize returns the total size of all layers
func (m *OCIManifest) GetTotalSize() int64 {
	var total int64
	total = m.Config.Size
	for _, layer := range m.Layers {
		total += layer.Size
	}
	return total
}
