package version

// Variables injected at build time via ldflags
var (
	// Version is the semantic version (from Chart.yaml appVersion or tag)
	Version = "dev"
	// Commit is the git commit SHA
	Commit = "unknown"
	// BuildTime is when the binary was built
	BuildTime = "unknown"
)

// Info returns version information as a map
func Info() map[string]string {
	return map[string]string{
		"version":   Version,
		"commit":    Commit,
		"buildTime": BuildTime,
	}
}

// String returns the version tag (e.g. "v1.0.0")
func String() string {
	return Version
}

// StringWithCommit returns version with short commit hash (e.g. "v1.0.0-091fa6d")
func StringWithCommit() string {
	if Commit == "unknown" || Commit == "" {
		return Version
	}
	shortCommit := Commit
	if len(Commit) > 7 {
		shortCommit = Commit[:7]
	}
	return Version + "-" + shortCommit
}
