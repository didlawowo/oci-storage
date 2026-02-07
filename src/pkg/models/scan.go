package models

import "time"

// ScanResult represents the result of a Trivy vulnerability scan
type ScanResult struct {
	Digest          string          `json:"digest"`
	ImageName       string          `json:"imageName"`
	Tag             string          `json:"tag"`
	ScannedAt       time.Time       `json:"scannedAt"`
	Critical        int             `json:"critical"`
	High            int             `json:"high"`
	Medium          int             `json:"medium"`
	Low             int             `json:"low"`
	Vulnerabilities []Vulnerability `json:"vulnerabilities"`
}

// Vulnerability represents a single CVE found by Trivy
type Vulnerability struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Package     string `json:"package"`
	Version     string `json:"version"`
	FixedIn     string `json:"fixedIn"`
	Description string `json:"description"`
	Link        string `json:"link"`
}

// ScanDecision represents an admin's decision on a scanned image
type ScanDecision struct {
	Digest     string      `json:"digest"`
	ImageName  string      `json:"imageName"`
	Tag        string      `json:"tag"`
	Status     string      `json:"status"` // approved, pending, denied
	Reason     string      `json:"reason"`
	DecidedBy  string      `json:"decidedBy"`
	DecidedAt  time.Time   `json:"decidedAt"`
	ExpiresAt  *time.Time  `json:"expiresAt,omitempty"`
	ScanResult *ScanResult `json:"scanResult,omitempty"`
}

// ScanSummary provides aggregate scan statistics
type ScanSummary struct {
	TotalScanned int `json:"totalScanned"`
	Pending      int `json:"pending"`
	Approved     int `json:"approved"`
	Denied       int `json:"denied"`
	Critical     int `json:"critical"`
	High         int `json:"high"`
}

// TrivyReport represents the JSON output from Trivy scanner
type TrivyReport struct {
	Results []TrivyResult `json:"Results"`
}

// TrivyResult represents a single target result from Trivy
type TrivyResult struct {
	Target          string               `json:"Target"`
	Type            string               `json:"Type"`
	Vulnerabilities []TrivyVulnerability `json:"Vulnerabilities"`
}

// TrivyVulnerability represents a vulnerability as reported by Trivy
type TrivyVulnerability struct {
	VulnerabilityID  string `json:"VulnerabilityID"`
	PkgName          string `json:"PkgName"`
	InstalledVersion string `json:"InstalledVersion"`
	FixedVersion     string `json:"FixedVersion"`
	Severity         string `json:"Severity"`
	Description      string `json:"Description"`
	PrimaryURL       string `json:"PrimaryURL"`
}
