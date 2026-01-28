package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oci-storage/config"
	"oci-storage/pkg/models"
	"oci-storage/pkg/utils"

	"github.com/sirupsen/logrus"
)

// ScanService handles Trivy vulnerability scanning and security gate decisions
type ScanService struct {
	config      *config.Config
	log         *utils.Logger
	pathManager *utils.PathManager
	scanMutex   sync.RWMutex
	scanSem     chan struct{} // limits concurrent scans
}

// NewScanService creates a new ScanService
func NewScanService(cfg *config.Config, log *utils.Logger, pathManager *utils.PathManager) *ScanService {
	// Create scan results and decisions directories
	scanResultsDir := filepath.Join(cfg.Storage.Path, "scan-results")
	scanDecisionsDir := filepath.Join(cfg.Storage.Path, "scan-decisions")
	os.MkdirAll(scanResultsDir, 0755)
	os.MkdirAll(scanDecisionsDir, 0755)

	return &ScanService{
		config:      cfg,
		log:         log,
		pathManager: pathManager,
		scanSem:     make(chan struct{}, 3), // max 3 concurrent scans
	}
}

// IsEnabled returns whether scanning is enabled
func (s *ScanService) IsEnabled() bool {
	return s.config.Trivy.Enabled
}

// ScanImage triggers an async vulnerability scan for the given image
func (s *ScanService) ScanImage(name, ref, digest string) {
	if !s.IsEnabled() {
		return
	}

	// Check if already scanned (and not expired)
	if result, err := s.GetScanResult(digest); err == nil && result != nil {
		ttl := s.config.Trivy.Policy.TTLHours
		if ttl <= 0 {
			ttl = 24
		}
		if time.Since(result.ScannedAt) < time.Duration(ttl)*time.Hour {
			s.log.WithFunc().WithField("digest", digest).Debug("Scan result still valid, skipping")
			return
		}
	}

	go func() {
		// Acquire semaphore
		s.scanSem <- struct{}{}
		defer func() { <-s.scanSem }()

		s.log.WithFunc().WithFields(logrus.Fields{
			"name":   name,
			"ref":    ref,
			"digest": digest,
		}).Info("Starting vulnerability scan")

		result, err := s.executeScan(name, ref, digest)
		if err != nil {
			s.log.WithFunc().WithError(err).WithField("digest", digest).Error("Scan failed")
			return
		}

		// Save scan result
		if err := s.saveScanResult(result); err != nil {
			s.log.WithFunc().WithError(err).Error("Failed to save scan result")
			return
		}

		// Evaluate policy and create decision
		status := s.EvaluatePolicy(result)
		s.log.WithFunc().WithFields(logrus.Fields{
			"digest":   digest,
			"status":   status,
			"critical": result.Critical,
			"high":     result.High,
		}).Info("Scan completed")

		// Auto-create decision
		if status == "approved" {
			s.SetDecision(digest, "approved", "Auto-approved: within policy thresholds", "system", 0)
		} else {
			// Create pending decision
			s.SetDecision(digest, "pending", "Exceeds policy thresholds, awaiting review", "system", 0)
		}
	}()
}

// executeScan runs Trivy CLI in client-server mode to scan an image.
// In this mode, the Trivy CLI runs locally and connects to the Trivy server
// (sidecar) for the vulnerability database. Scanning happens client-side.
// It uses "trivy image --server <url>" to scan by image reference.
func (s *ScanService) executeScan(name, ref, digest string) (*models.ScanResult, error) {
	trivyURL := s.config.Trivy.ServerURL
	if trivyURL == "" {
		trivyURL = "http://localhost:4954"
	}

	// Build the image reference pointing to our own registry so Trivy
	// pulls layers from localhost, not from the upstream registry.
	registryHost := fmt.Sprintf("localhost:%d", s.config.Server.Port)
	var imageRef string
	if strings.HasPrefix(digest, "sha256:") {
		imageRef = fmt.Sprintf("%s/%s@%s", registryHost, name, digest)
	} else {
		imageRef = fmt.Sprintf("%s/%s:%s", registryHost, name, ref)
	}

	// Build trivy command arguments
	args := []string{
		"image",
		"--server", trivyURL,
		"--format", "json",
		"--severity", "CRITICAL,HIGH,MEDIUM,LOW",
		"--no-progress",
		"--insecure",
		imageRef,
	}

	s.log.WithFunc().WithFields(logrus.Fields{
		"command": "trivy " + strings.Join(args, " "),
	}).Debug("Executing Trivy scan")

	var stdout, stderr bytes.Buffer
	cmd := exec.Command("trivy", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Trivy exits non-zero when vulnerabilities are found — that's expected.
		// Only treat as error if we got no JSON output at all.
		if stdout.Len() == 0 {
			return nil, fmt.Errorf("trivy scan failed: %w, stderr: %s", err, stderr.String())
		}
		s.log.WithFunc().WithField("stderr", stderr.String()).Debug("Trivy exited non-zero (vulnerabilities found)")
	}

	// Parse Trivy JSON output
	var trivyReport models.TrivyReport
	if err := json.Unmarshal(stdout.Bytes(), &trivyReport); err != nil {
		return nil, fmt.Errorf("failed to parse Trivy output: %w, raw: %s", err, stdout.String())
	}

	// Convert to our ScanResult model
	result := &models.ScanResult{
		Digest:    digest,
		ImageName: name,
		Tag:       ref,
		ScannedAt: time.Now(),
	}

	allowlist := make(map[string]bool)
	for _, cve := range s.config.Trivy.Policy.Allowlist {
		allowlist[cve] = true
	}

	for _, trivyResult := range trivyReport.Results {
		for _, vuln := range trivyResult.Vulnerabilities {
			if allowlist[vuln.VulnerabilityID] {
				continue
			}

			v := models.Vulnerability{
				ID:          vuln.VulnerabilityID,
				Severity:    vuln.Severity,
				Package:     vuln.PkgName,
				Version:     vuln.InstalledVersion,
				FixedIn:     vuln.FixedVersion,
				Description: vuln.Description,
				Link:        vuln.PrimaryURL,
			}
			result.Vulnerabilities = append(result.Vulnerabilities, v)

			switch strings.ToUpper(vuln.Severity) {
			case "CRITICAL":
				result.Critical++
			case "HIGH":
				result.High++
			case "MEDIUM":
				result.Medium++
			case "LOW":
				result.Low++
			}
		}
	}

	return result, nil
}

// EvaluatePolicy checks scan results against configured policy thresholds
// A threshold of 0 means disabled (no limit), except for MaxCritical where 0 means none tolerated
func (s *ScanService) EvaluatePolicy(result *models.ScanResult) string {
	policy := s.config.Trivy.Policy

	// Check exempt images
	for _, exempt := range policy.ExemptImages {
		if strings.HasPrefix(result.ImageName, exempt) {
			return "approved"
		}
	}

	total := result.Critical + result.High + result.Medium + result.Low

	// MaxCritical: 0 means no criticals tolerated, >0 means that many allowed
	if result.Critical > policy.MaxCritical {
		return "pending"
	}
	// MaxHigh: 0 means disabled (no limit), >0 means that many allowed
	if policy.MaxHigh > 0 && result.High > policy.MaxHigh {
		return "pending"
	}
	// MaxTotal: 0 means disabled (no limit), >0 means that many allowed
	if policy.MaxTotal > 0 && total > policy.MaxTotal {
		return "pending"
	}

	return "approved"
}

// GetScanResult returns the scan result for a given digest
func (s *ScanService) GetScanResult(digest string) (*models.ScanResult, error) {
	path := s.scanResultPath(digest)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var result models.ScanResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// saveScanResult writes a scan result to disk
func (s *ScanService) saveScanResult(result *models.ScanResult) error {
	path := s.scanResultPath(result.Digest)
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// GetDecision returns the security gate decision for a given digest
func (s *ScanService) GetDecision(digest string) (*models.ScanDecision, error) {
	s.scanMutex.RLock()
	defer s.scanMutex.RUnlock()

	decisions, err := s.loadDecisions()
	if err != nil {
		return nil, err
	}

	for _, d := range decisions {
		if d.Digest == digest {
			// Check expiry
			if d.ExpiresAt != nil && time.Now().After(*d.ExpiresAt) {
				// Expired — treat as pending
				expired := d
				expired.Status = "pending"
				expired.Reason = "Approval expired, awaiting re-review"
				return &expired, nil
			}
			return &d, nil
		}
	}

	return nil, fmt.Errorf("no decision found for digest %s", digest)
}

// SetDecision sets the security gate decision for a given digest
func (s *ScanService) SetDecision(digest, status, reason, decidedBy string, expiresInDays int) error {
	s.scanMutex.Lock()
	defer s.scanMutex.Unlock()

	decisions, err := s.loadDecisions()
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Load scan result to attach image info
	scanResult, _ := s.GetScanResult(digest)

	decision := models.ScanDecision{
		Digest:    digest,
		Status:    status,
		Reason:    reason,
		DecidedBy: decidedBy,
		DecidedAt: time.Now(),
	}

	if scanResult != nil {
		decision.ImageName = scanResult.ImageName
		decision.Tag = scanResult.Tag
		decision.ScanResult = scanResult
	}

	if expiresInDays > 0 {
		expires := time.Now().Add(time.Duration(expiresInDays) * 24 * time.Hour)
		decision.ExpiresAt = &expires
	}

	// Update or append
	found := false
	for i, d := range decisions {
		if d.Digest == digest {
			decisions[i] = decision
			found = true
			break
		}
	}
	if !found {
		decisions = append(decisions, decision)
	}

	return s.saveDecisions(decisions)
}

// ListPendingDecisions returns all images awaiting review
func (s *ScanService) ListPendingDecisions() ([]models.ScanDecision, error) {
	s.scanMutex.RLock()
	defer s.scanMutex.RUnlock()

	decisions, err := s.loadDecisions()
	if err != nil {
		if os.IsNotExist(err) {
			return []models.ScanDecision{}, nil
		}
		return nil, err
	}

	var pending []models.ScanDecision
	for _, d := range decisions {
		isPending := d.Status == "pending"
		isExpired := d.ExpiresAt != nil && time.Now().After(*d.ExpiresAt)
		if isPending || isExpired {
			if isExpired {
				d.Status = "pending"
				d.Reason = "Approval expired, awaiting re-review"
			}
			pending = append(pending, d)
		}
	}
	return pending, nil
}

// ListAllDecisions returns all decisions
func (s *ScanService) ListAllDecisions() ([]models.ScanDecision, error) {
	s.scanMutex.RLock()
	defer s.scanMutex.RUnlock()

	decisions, err := s.loadDecisions()
	if err != nil {
		if os.IsNotExist(err) {
			return []models.ScanDecision{}, nil
		}
		return nil, err
	}
	return decisions, nil
}

// DeleteDecision removes a decision, forcing re-review
func (s *ScanService) DeleteDecision(digest string) error {
	s.scanMutex.Lock()
	defer s.scanMutex.Unlock()

	decisions, err := s.loadDecisions()
	if err != nil {
		return err
	}

	var filtered []models.ScanDecision
	for _, d := range decisions {
		if d.Digest != digest {
			filtered = append(filtered, d)
		}
	}

	return s.saveDecisions(filtered)
}

// GetSummary returns aggregate scan statistics
func (s *ScanService) GetSummary() (*models.ScanSummary, error) {
	decisions, err := s.loadDecisions()
	if err != nil {
		if os.IsNotExist(err) {
			return &models.ScanSummary{}, nil
		}
		return nil, err
	}

	summary := &models.ScanSummary{
		TotalScanned: len(decisions),
	}
	for _, d := range decisions {
		switch d.Status {
		case "pending":
			summary.Pending++
		case "approved":
			summary.Approved++
		case "denied":
			summary.Denied++
		}
		if d.ScanResult != nil {
			summary.Critical += d.ScanResult.Critical
			summary.High += d.ScanResult.High
		}
	}
	return summary, nil
}

// Helper methods

func (s *ScanService) scanResultPath(digest string) string {
	// Sanitize digest for filesystem
	safe := strings.ReplaceAll(digest, ":", "-")
	return filepath.Join(s.config.Storage.Path, "scan-results", safe+".json")
}

func (s *ScanService) decisionsPath() string {
	return filepath.Join(s.config.Storage.Path, "scan-decisions", "decisions.json")
}

func (s *ScanService) loadDecisions() ([]models.ScanDecision, error) {
	data, err := os.ReadFile(s.decisionsPath())
	if err != nil {
		return nil, err
	}

	var decisions []models.ScanDecision
	if err := json.Unmarshal(data, &decisions); err != nil {
		return nil, err
	}
	return decisions, nil
}

func (s *ScanService) saveDecisions(decisions []models.ScanDecision) error {
	data, err := json.MarshalIndent(decisions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.decisionsPath(), data, 0644)
}
