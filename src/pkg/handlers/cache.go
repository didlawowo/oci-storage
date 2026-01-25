// pkg/handlers/cache.go
package handlers

import (
	"net/url"
	"strings"

	"oci-storage/pkg/interfaces"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

// CacheHandler handles cache management HTTP requests
type CacheHandler struct {
	log          *utils.Logger
	proxyService interfaces.ProxyServiceInterface
}

// NewCacheHandler creates a new cache handler
func NewCacheHandler(proxyService interfaces.ProxyServiceInterface, log *utils.Logger) *CacheHandler {
	return &CacheHandler{
		proxyService: proxyService,
		log:          log,
	}
}

// GetCacheStatus returns cache statistics
func (h *CacheHandler) GetCacheStatus(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Getting cache status")

	if h.proxyService == nil || !h.proxyService.IsEnabled() {
		return c.JSON(fiber.Map{
			"enabled": false,
		})
	}

	state := h.proxyService.GetCacheState()

	return c.JSON(fiber.Map{
		"enabled":      true,
		"totalSize":    state.TotalSize,
		"maxSize":      state.MaxSize,
		"itemCount":    state.ItemCount,
		"usagePercent": state.UsagePercent,
	})
}

// ListCachedImages returns all cached images with metadata
func (h *CacheHandler) ListCachedImages(c *fiber.Ctx) error {
	h.log.WithFunc().Debug("Listing cached images")

	if h.proxyService == nil || !h.proxyService.IsEnabled() {
		return c.JSON(fiber.Map{
			"images": []interface{}{},
		})
	}

	images, err := h.proxyService.GetCachedImages()
	if err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to list cached images")
		return HTTPError(c, 500, err.Error())
	}

	return c.JSON(fiber.Map{
		"images": images,
	})
}

// PurgeCache clears all cached images
func (h *CacheHandler) PurgeCache(c *fiber.Ctx) error {
	h.log.WithFunc().Info("Purging cache")

	if h.proxyService == nil || !h.proxyService.IsEnabled() {
		return HTTPError(c, 400, "Proxy not enabled")
	}

	if err := h.proxyService.PurgeAllCache(); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to purge cache")
		return HTTPError(c, 500, err.Error())
	}

	return c.JSON(fiber.Map{
		"message": "Cache purged",
	})
}

// DeleteCachedImageWildcard handles DELETE /cache/image/* with wildcard path parsing
// Path format: /cache/image/proxy/docker.io/traefik/v3.2 -> name=proxy/docker.io/traefik, tag=v3.2
func (h *CacheHandler) DeleteCachedImageWildcard(c *fiber.Ctx) error {
	path := c.Params("*")

	// Split to get name and tag - tag is the last segment
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return HTTPError(c, 400, "Invalid image path format - expected name/tag")
	}

	name, _ := url.PathUnescape(path[:lastSlash])
	tag, _ := url.PathUnescape(path[lastSlash+1:])

	h.log.WithFunc().WithField("name", name).WithField("tag", tag).Debug("Deleting cached image (wildcard)")

	if h.proxyService == nil || !h.proxyService.IsEnabled() {
		return HTTPError(c, 400, "Proxy not enabled")
	}

	if err := h.proxyService.DeleteCachedImage(name, tag); err != nil {
		h.log.WithFunc().WithError(err).Error("Failed to delete cached image")
		return HTTPError(c, 500, err.Error())
	}

	return c.JSON(fiber.Map{
		"message": "Cached image deleted",
		"name":    name,
		"tag":     tag,
	})
}
