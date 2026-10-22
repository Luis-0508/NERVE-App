package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/nerve-labs/nerve/internal/release"
)

// runVerifyRelease checks a downloaded release directory against the release
// keys compiled into this agent, and that it contains a build for this host.
func runVerifyRelease(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: nerve-agent verify-release DIR")
		return 2
	}
	manifest, err := release.VerifyDirectory(release.PinnedKeys(), args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "release verification failed:", err)
		return 1
	}
	agent, err := manifest.AgentFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release verification failed:", err)
		return 1
	}
	fmt.Printf("Release %s verified: %d files match the signed manifest; %s is the build for this host.\n",
		manifest.Version, len(manifest.Files), agent.Name)
	return 0
}
