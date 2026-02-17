package coordination

import (
	"context"
	"time"
)

// LockManager provides distributed locking for multi-replica coordination.
// Implementations must be safe for concurrent use.
type LockManager interface {
	// Acquire attempts to acquire a named lock with a TTL.
	// Returns an unlock function that MUST be called when done (typically via defer).
	// The lock auto-expires after TTL as a safety net against crashes.
	// Returns an error if the lock is already held.
	Acquire(ctx context.Context, key string, ttl time.Duration) (unlock func(), err error)
}

// UploadTracker tracks chunked upload sessions across replicas.
// This ensures that all PATCH/PUT requests for a given upload UUID
// are routed to the same pod that started the upload.
type UploadTracker interface {
	// Register records that this pod owns the given upload session.
	Register(ctx context.Context, uuid string, ttl time.Duration) error
	// CheckOwnership verifies this pod owns the upload.
	// Returns nil if: not tracked (noop mode) OR owned by this pod.
	// Returns error if owned by a different pod.
	CheckOwnership(ctx context.Context, uuid string) error
	// Remove deletes the upload session tracking entry.
	Remove(ctx context.Context, uuid string) error
}

// ScanTracker deduplicates vulnerability scans across replicas.
// Ensures only one pod scans a given digest at a time.
type ScanTracker interface {
	// ClaimScan attempts to claim ownership of scanning a digest.
	// Returns true if this pod got the claim (should proceed with scan).
	// Returns false if another pod is already scanning it.
	ClaimScan(ctx context.Context, digest string, ttl time.Duration) bool
	// ReleaseScan releases the scan claim after completion.
	ReleaseScan(ctx context.Context, digest string)
	// IsScanRunning returns true if any pod is currently scanning this digest.
	IsScanRunning(ctx context.Context, digest string) bool
}

// --- Noop implementations for single-replica mode ---

// NoopLockManager always succeeds immediately (no distributed coordination).
type NoopLockManager struct{}

func (n *NoopLockManager) Acquire(_ context.Context, _ string, _ time.Duration) (func(), error) {
	return func() {}, nil
}

// NoopUploadTracker accepts all uploads on any pod (single-replica mode).
type NoopUploadTracker struct{}

func (n *NoopUploadTracker) Register(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (n *NoopUploadTracker) CheckOwnership(_ context.Context, _ string) error {
	return nil
}

func (n *NoopUploadTracker) Remove(_ context.Context, _ string) error {
	return nil
}

// NoopScanTracker never deduplicates (single-replica falls back to local map).
type NoopScanTracker struct{}

func (n *NoopScanTracker) ClaimScan(_ context.Context, _ string, _ time.Duration) bool {
	return true // always allow
}

func (n *NoopScanTracker) ReleaseScan(_ context.Context, _ string) {}

func (n *NoopScanTracker) IsScanRunning(_ context.Context, _ string) bool {
	return false // no cross-pod visibility
}
