package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oci-storage/pkg/models"

	"github.com/gofiber/fiber/v2"
	"github.com/sirupsen/logrus"
)

// sendManifestResponse sends a manifest response to the client
// For manifest lists/indexes, it conditionally resolves to a platform-specific manifest based on:
// - Reference type: only resolve for tags, never for digests (client expects exact content)
// - Accept header: only resolve if client doesn't support manifest lists
func (h *OCIHandler) sendManifestResponse(c *fiber.Ctx, manifestData []byte, reference string) error {
	h.log.WithFunc().WithField("dataLen", len(manifestData)).Debug("sendManifestResponse called")

	// Determine the digest to use
	// If the reference is already a digest, trust it (the file was found by this digest)
	// This handles cases where JSON re-serialization changed the content hash
	// but the file was correctly stored with the original digest as filename
	isDigestRef := strings.HasPrefix(reference, "sha256:")
	var digest string
	if isDigestRef {
		// Trust the requested digest - the file was found by this name
		digest = reference
	} else {
		// Calculate digest for tag references
		digest = fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))
	}

	// Determine content type from manifest
	var manifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err == nil && manifest.MediaType != "" {
		// Check if this is a manifest list/index
		isManifestList := manifest.MediaType == models.MediaTypeOCIManifestList ||
			manifest.MediaType == models.MediaTypeDockerManifestList

		if isManifestList {
			// Decide whether to resolve to platform-specific manifest
			// NEVER resolve if client requested by digest - they know what they want
			// Only resolve if client doesn't support manifest lists (based on Accept header)
			clientSupportsIndex := h.clientSupportsManifestList(c)

			h.log.WithFields(logrus.Fields{
				"isDigestRef":         isDigestRef,
				"clientSupportsIndex": clientSupportsIndex,
				"mediaType":           manifest.MediaType,
			}).Debug("Manifest list detected, checking if resolution needed")

			shouldResolve := !isDigestRef && !clientSupportsIndex

			if shouldResolve {
				resolvedData, resolvedDigest, err := h.resolvePlatformManifest(c, manifestData)
				if err == nil && resolvedData != nil {
					h.log.WithFields(logrus.Fields{
						"originalDigest": digest,
						"resolvedDigest": resolvedDigest,
						"resolvedSize":   len(resolvedData),
					}).Info("Resolved manifest list to platform-specific manifest")

					manifestData = resolvedData
					digest = resolvedDigest

					var resolvedManifest models.OCIManifest
					if err := json.Unmarshal(manifestData, &resolvedManifest); err == nil && resolvedManifest.MediaType != "" {
						c.Set("Content-Type", resolvedManifest.MediaType)
					} else {
						c.Set("Content-Type", models.MediaTypeOCIManifest)
					}
				} else {
					h.log.WithError(err).Debug("Failed to resolve platform manifest, returning index")
					c.Set("Content-Type", manifest.MediaType)
				}
			} else {
				// Client supports manifest lists or requested by digest - return as-is
				c.Set("Content-Type", manifest.MediaType)
			}
		} else {
			c.Set("Content-Type", manifest.MediaType)
		}
	} else {
		c.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	}

	c.Set("Docker-Content-Digest", digest)

	if c.Method() == "HEAD" {
		c.Set("Content-Length", fmt.Sprintf("%d", len(manifestData)))
		return c.Status(200).Send(nil)
	}

	return c.Send(manifestData)
}

// clientSupportsManifestList checks if the client supports manifest lists based on Accept header
func (h *OCIHandler) clientSupportsManifestList(c *fiber.Ctx) bool {
	accept := c.Get("Accept")

	// If no Accept header, assume modern client that supports manifest lists
	if accept == "" {
		return true
	}

	// Check for manifest list/index media types in Accept header
	supportsIndex := strings.Contains(accept, models.MediaTypeOCIManifestList) ||
		strings.Contains(accept, models.MediaTypeDockerManifestList) ||
		strings.Contains(accept, "*/*")

	h.log.WithFields(logrus.Fields{
		"accept":        accept,
		"supportsIndex": supportsIndex,
	}).Debug("Checking client manifest list support")

	return supportsIndex
}

// resolvePlatformManifest resolves a manifest list/index to a platform-specific manifest
func (h *OCIHandler) resolvePlatformManifest(c *fiber.Ctx, indexData []byte) ([]byte, string, error) {
	var index models.OCIIndex
	if err := json.Unmarshal(indexData, &index); err != nil {
		return nil, "", fmt.Errorf("failed to parse manifest index: %w", err)
	}

	preferredOS, preferredArch := h.detectClientPlatform(c)

	h.log.WithFields(logrus.Fields{
		"preferredOS":   preferredOS,
		"preferredArch": preferredArch,
		"manifestCount": len(index.Manifests),
	}).Debug("Resolving platform manifest")

	var matchingDesc *models.OCIDescriptor
	var fallbackDesc *models.OCIDescriptor

	for i := range index.Manifests {
		desc := &index.Manifests[i]
		if desc.Platform == nil {
			continue
		}

		if desc.Platform.OS == preferredOS && desc.Platform.Architecture == preferredArch {
			matchingDesc = desc
			break
		}

		if fallbackDesc == nil && desc.Platform.OS == "linux" && desc.Platform.Architecture == "amd64" {
			fallbackDesc = desc
		}
	}

	if matchingDesc == nil {
		matchingDesc = fallbackDesc
	}

	if matchingDesc == nil {
		return nil, "", fmt.Errorf("no matching platform manifest found for %s/%s", preferredOS, preferredArch)
	}

	h.log.WithFields(logrus.Fields{
		"platform": matchingDesc.Platform.OS + "/" + matchingDesc.Platform.Architecture,
		"digest":   matchingDesc.Digest,
	}).Debug("Found matching platform manifest")

	// Try to fetch from cache first
	blobPath := h.pathManager.GetBlobPath(matchingDesc.Digest)
	manifestData, err := os.ReadFile(blobPath)
	if err == nil {
		return manifestData, matchingDesc.Digest, nil
	}

	// Not in cache - fetch on-demand from upstream if proxy is enabled
	if h.proxyService == nil || !h.proxyService.IsEnabled() {
		return nil, "", fmt.Errorf("platform manifest not in cache and proxy not enabled: %w", err)
	}

	name := h.getName(c)

	h.log.WithFields(logrus.Fields{
		"name":     name,
		"platform": matchingDesc.Platform.OS + "/" + matchingDesc.Platform.Architecture,
		"digest":   matchingDesc.Digest,
	}).Info("Platform manifest not in cache, fetching on-demand")

	registryURL, upstreamName, err := h.proxyService.ResolveRegistry(name)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve registry: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manifestData, _, err = h.proxyService.GetManifest(ctx, registryURL, upstreamName, matchingDesc.Digest)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch platform manifest from upstream: %w", err)
	}

	// Cache the manifest for future requests
	if err := os.MkdirAll(filepath.Dir(blobPath), 0755); err == nil {
		if err := os.WriteFile(blobPath, manifestData, 0644); err != nil {
			h.log.WithError(err).Warn("Failed to cache platform manifest")
		} else {
			h.log.WithFields(logrus.Fields{
				"platform": matchingDesc.Platform.OS + "/" + matchingDesc.Platform.Architecture,
				"digest":   matchingDesc.Digest,
			}).Info("Platform manifest fetched and cached on-demand")
		}
	}

	return manifestData, matchingDesc.Digest, nil
}

// detectClientPlatform detects the client's platform preference from headers
func (h *OCIHandler) detectClientPlatform(c *fiber.Ctx) (string, string) {
	preferredOS := "linux"
	preferredArch := "amd64"

	userAgent := c.Get("User-Agent")

	if strings.Contains(strings.ToLower(userAgent), "arm64") ||
		strings.Contains(strings.ToLower(userAgent), "aarch64") {
		preferredArch = "arm64"
	}

	accept := c.Get("Accept")
	h.log.WithFields(logrus.Fields{
		"userAgent": userAgent,
		"accept":    accept,
	}).Debug("Detecting client platform")

	return preferredOS, preferredArch
}
