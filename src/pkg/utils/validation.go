package utils

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// OCI specification compliant validation patterns
var (
	// Digest must be sha256:<64 hex chars> or sha512:<128 hex chars>
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$|^sha512:[a-f0-9]{128}$`)

	// Repository name: lowercase alphanumeric, dots, dashes, underscores, slashes
	// Max 255 chars, no leading/trailing slashes, no double slashes
	repoNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)

	// Tag: alphanumeric, dots, dashes, underscores, max 128 chars
	// Must start with alphanumeric or underscore
	tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

	// UUID: standard format with dashes
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidateDigest validates that a digest follows OCI specification
// Returns error if invalid, nil if valid
func ValidateDigest(digest string) error {
	if digest == "" {
		return fmt.Errorf("digest cannot be empty")
	}
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("invalid digest format: must be sha256:<64 hex chars>")
	}
	return nil
}

// ValidateRepoName validates repository name follows OCI specification
// Returns error if invalid, nil if valid
func ValidateRepoName(name string) error {
	if name == "" {
		return fmt.Errorf("repository name cannot be empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("repository name too long: max 255 characters")
	}
	if !repoNamePattern.MatchString(name) {
		return fmt.Errorf("invalid repository name: must be lowercase alphanumeric with optional dots, dashes, underscores, slashes")
	}
	return nil
}

// ValidateTag validates that a tag follows OCI specification
// Returns error if invalid, nil if valid
func ValidateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("tag cannot be empty")
	}
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid tag format: must start with alphanumeric or underscore, max 128 chars")
	}
	return nil
}

// ValidateReference validates a reference which can be either a tag or digest
// Returns error if invalid, nil if valid
func ValidateReference(reference string) error {
	if reference == "" {
		return fmt.Errorf("reference cannot be empty")
	}
	// If it looks like a digest, validate as digest
	if len(reference) > 7 && reference[:7] == "sha256:" {
		return ValidateDigest(reference)
	}
	if len(reference) > 7 && reference[:7] == "sha512:" {
		return ValidateDigest(reference)
	}
	// Otherwise validate as tag
	return ValidateTag(reference)
}

// ValidateUUID validates UUID format
// Returns error if invalid, nil if valid
func ValidateUUID(uuid string) error {
	if uuid == "" {
		return fmt.Errorf("UUID cannot be empty")
	}
	if !uuidPattern.MatchString(uuid) {
		return fmt.Errorf("invalid UUID format")
	}
	return nil
}

// ComputeFileDigest computes the sha256 digest of a file by streaming it
// without loading the entire file into memory.
func ComputeFileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file for digest: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to compute digest: %w", err)
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// ValidateManifestContent performs minimal structural validation of an OCI manifest.
// Only rejects clearly malformed payloads. Does NOT restrict mediaType because the
// OCI spec allows arbitrary artifact types (Helm, WASM, SBOM, Cosign signatures, etc.).
func ValidateManifestContent(manifest map[string]interface{}) error {
	// schemaVersion must be present and equal to 2
	sv, ok := manifest["schemaVersion"]
	if !ok {
		return fmt.Errorf("MANIFEST_INVALID: missing schemaVersion field")
	}
	svFloat, ok := sv.(float64)
	if !ok || svFloat != 2 {
		return fmt.Errorf("MANIFEST_INVALID: schemaVersion must be 2, got %v", sv)
	}

	// Must have either "manifests" (index) or "config" (image/artifact manifest)
	_, hasManifests := manifest["manifests"]
	_, hasConfig := manifest["config"]
	if !hasManifests && !hasConfig {
		return fmt.Errorf("MANIFEST_INVALID: must contain either 'config' or 'manifests' field")
	}

	// For non-index manifests, config must be an object with a digest
	if !hasManifests && hasConfig {
		cfgMap, ok := manifest["config"].(map[string]interface{})
		if !ok {
			return fmt.Errorf("MANIFEST_INVALID: config must be an object")
		}
		if _, hasDigest := cfgMap["digest"]; !hasDigest {
			return fmt.Errorf("MANIFEST_INVALID: config.digest is required")
		}
	}

	return nil
}

// AtomicWriteFile writes data to a file atomically by writing to a temp file
// first and then renaming. This prevents corruption from concurrent reads
// during write, crashes, or power loss.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to set file permissions: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
