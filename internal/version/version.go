package version

// Version is the current version of the agent.
// This will be set during build time via ldflags:
// go build -ldflags="-X github.com/k0wl0n/agent-backup/internal/version.Version=1.2.3"
var Version = "dev"

// BuildTime is the build timestamp (set via ldflags)
var BuildTime = "unknown"

// GitCommit is the git commit hash (set via ldflags)
var GitCommit = "unknown"

// GetVersion returns the full version string
func GetVersion() string {
	if Version == "dev" {
		return "dev (development build)"
	}
	return Version
}

// GetFullVersion returns version with build details
func GetFullVersion() string {
	if Version == "dev" {
		return "jokowipe-agent dev (development build)"
	}
	return "jokowipe-agent " + Version
}
