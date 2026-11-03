//go:build !linux && !windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
)

// Other platforms do not yet provide a privileged helper channel.

func helperSupported() bool { return false }

func newLocalHelperClient(string, *slog.Logger) privilegedRunner { return nil }

func runHelper([]string) int {
	fmt.Fprintln(os.Stderr, "the privileged helper is not available on this platform yet")
	return 1
}

func diagnoseHelper(_ context.Context, result *diagnosis, _ string) {
	result.add("helper", "skip", "not available on this platform yet; privileged commands fail closed")
}
