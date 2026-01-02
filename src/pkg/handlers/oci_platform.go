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
// For manifest lists/indexes, we return the list as-is and let the client handle platform selection
// This is the standard OCI behavior - clients like containerd know how to handle manifest lists
func (h *OCIHandler) sendManifestResponse(c *fiber.Ctx, manifestData []byte, reference string) error {
	h.log.WithFunc().WithField("dataLen", len(manifestData)).Debug("sendManifestResponse called")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifestData))

	// Verify digest if reference is a digest
	if strings.HasPrefix(reference, "sha256:") && digest != reference {
		h.log.WithFunc().WithFields(logrus.Fields{
			"expected": reference,
			"got":      digest,
		}).Error("Manifest digest mismatch")
		return c.SendStatus(404)
	}

	// Determine content type from manifest
	var manifest models.OCIManifest
	if err := json.Unmarshal(manifestData, &manifest); err == nil && manifest.MediaType != "" {
		c.Set("Content-Type", manifest.MediaType)
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
