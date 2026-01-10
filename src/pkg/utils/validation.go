package utils

import (
	"fmt"
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
