// pkg/models/cache.go
package models

import "time"

// CachedImageMetadata tracks cached image information for LRU eviction
type CachedImageMetadata struct {
	Name           string    `json:"name"`
	Tag            string    `json:"tag"`
	Digest         string    `json:"digest"`
	SourceRegistry string    `json:"sourceRegistry"` // e.g., "docker.io"
	OriginalRef    string    `json:"originalRef"`    // e.g., "library/alpine:latest"
	Size           int64     `json:"size"`
	CachedAt       time.Time `json:"cachedAt"`
	LastAccessed   time.Time `json:"lastAccessed"`
	AccessCount    int64     `json:"accessCount"`
}

// CacheState represents the overall cache state
type CacheState struct {
	TotalSize    int64                 `json:"totalSize"`
	MaxSize      int64                 `json:"maxSize"`
	ItemCount    int                   `json:"itemCount"`
	UsagePercent float64               `json:"usagePercent"`
	Images       []CachedImageMetadata `json:"images,omitempty"`
}

// CalculateUsagePercent calculates and sets the usage percentage
func (cs *CacheState) CalculateUsagePercent() {
	if cs.MaxSize > 0 {
		cs.UsagePercent = float64(cs.TotalSize) / float64(cs.MaxSize) * 100
	} else {
		cs.UsagePercent = 0
	}
}
