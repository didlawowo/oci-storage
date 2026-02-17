package redis

import (
	"context"
	"fmt"
	"os"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/utils"

	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// Client wraps a Redis connection and implements LockManager + UploadTracker.
type Client struct {
	rdb   *goredis.Client
	log   *utils.Logger
	podID string
}

// NewClient creates and tests a Redis connection.
func NewClient(cfg config.RedisConfig, log *utils.Logger) (*Client, error) {
	rdb := goredis.NewClient(&goredis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	podID, _ := os.Hostname()
	if podID == "" {
		podID = fmt.Sprintf("unknown-%d", time.Now().UnixNano())
	}

	log.WithFields(logrus.Fields{
		"addr":  cfg.Addr,
		"db":    cfg.DB,
		"podID": podID,
	}).Info("Connected to Redis")

	return &Client{
		rdb:   rdb,
		log:   log,
		podID: podID,
	}, nil
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// --- LockManager implementation ---

// Acquire acquires a distributed lock using Redis SET NX with TTL.
// The lock auto-expires after TTL as a safety net against pod crashes.
func (c *Client) Acquire(ctx context.Context, key string, ttl time.Duration) (func(), error) {
	lockKey := "oci:lock:" + key

	ok, err := c.rdb.SetNX(ctx, lockKey, c.podID, ttl).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lock error: %w", err)
	}
	if !ok {
		owner, _ := c.rdb.Get(ctx, lockKey).Result()
		return nil, fmt.Errorf("lock %q held by pod %s", key, owner)
	}

	unlock := func() {
		// Only delete if we still own the lock (prevents deleting another pod's lock after TTL expiry)
		const luaReleaseLock = `
			if redis.call("GET", KEYS[1]) == ARGV[1] then
				return redis.call("DEL", KEYS[1])
			end
			return 0
		`
		c.rdb.Eval(context.Background(), luaReleaseLock, []string{lockKey}, c.podID)
	}

	return unlock, nil
}

// --- UploadTracker implementation ---

// Register records that this pod owns the given upload session.
func (c *Client) Register(ctx context.Context, uuid string, ttl time.Duration) error {
	key := "oci:upload:" + uuid
	return c.rdb.Set(ctx, key, c.podID, ttl).Err()
}

// CheckOwnership verifies this pod owns the upload.
// Returns nil if not tracked or owned by this pod.
// Returns error if owned by a different pod.
func (c *Client) CheckOwnership(ctx context.Context, uuid string) error {
	key := "oci:upload:" + uuid

	owner, err := c.rdb.Get(ctx, key).Result()
	if err == goredis.Nil {
		// Not tracked - this can happen if Redis was added after upload started
		return nil
	}
	if err != nil {
		// Redis error - log and allow (fail open to avoid blocking uploads)
		c.log.WithError(err).Warn("Redis error checking upload ownership, allowing request")
		return nil
	}

	if owner != c.podID {
		return fmt.Errorf("upload %s belongs to pod %s, this is pod %s", uuid, owner, c.podID)
	}
	return nil
}

// Remove deletes the upload session tracking entry.
func (c *Client) Remove(ctx context.Context, uuid string) error {
	key := "oci:upload:" + uuid
	return c.rdb.Del(ctx, key).Err()
}
