// pkg/handlers/cache.go
package handlers

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/interfaces"
	"oci-storage/pkg/storage"
	"oci-storage/pkg/utils"

	"github.com/gofiber/fiber/v2"
)

// CacheHandler handles cache management HTTP requests
type CacheHandler struct {
	log          *utils.Logger
	proxyService interfaces.ProxyServiceInterface
	pathManager  *utils.PathManager
	backend      storage.Backend
	cfg          *config.Config

	// usage cache — listing thousands of blobs on NFS is slow, refresh once per minute
	usageMu        sync.Mutex
	usageBytes     int64
	usageRefreshed time.Time
}

// NewCacheHandler creates a new cache handler
func NewCacheHandler(proxyService interfaces.ProxyServiceInterface, pathManager *utils.PathManager, backend storage.Backend, cfg *config.Config, log *utils.Logger) *CacheHandler {
	return &CacheHandler{
		proxyService: proxyService,
		pathManager:  pathManager,
		backend:      backend,
		cfg:          cfg,
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

	response := fiber.Map{
		"enabled":      true,
		"totalSize":    state.TotalSize,
		"maxSize":      state.MaxSize,
		"itemCount":    state.ItemCount,
		"usagePercent": state.UsagePercent,
	}

	// Disk usage: sum on-disk blobs + charts (NOT statfs, broken on NFS — see paths.go).
	// Total = STORAGE_QUOTA_BYTES injected from chart (matches PVC `size`).
	used := h.computeDiskUsed()
	total := h.cfg.Storage.QuotaBytes
	response["diskUsed"] = used
	if total > 0 {
		response["diskTotal"] = total
		response["diskAvailable"] = max(total-used, 0)
	}

	return c.JSON(response)
}

// computeDiskUsed returns the on-disk size of blobs + charts, cached for 60s.
// Cached images (proxy metadata) are NOT summed: their layers live in `blobs/` and would
// double-count.
func (h *CacheHandler) computeDiskUsed() int64 {
	h.usageMu.Lock()
	defer h.usageMu.Unlock()

	if time.Since(h.usageRefreshed) < time.Minute && !h.usageRefreshed.IsZero() {
		return h.usageBytes
	}

	var total int64
	if entries, err := h.backend.List("blobs"); err == nil {
		for _, e := range entries {
			if !e.IsDir {
				total += e.Size
			}
		}
	}
	if entries, err := h.backend.List("charts"); err == nil {
		for _, e := range entries {
			if !e.IsDir && strings.HasSuffix(e.Name, ".tgz") {
				total += e.Size
			}
		}
	}

	h.usageBytes = total
	h.usageRefreshed = time.Now()
	return total
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
