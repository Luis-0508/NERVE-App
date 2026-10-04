//go:build !windows

package main

import (
	"fmt"
	"os"
)

// securePrivateDir restricts the state directory to the owning account.
func securePrivateDir(dir string) error {
	return os.Chmod(dir, 0o700)
}

// checkPrivateFile refuses identity files that other accounts can read or
// modify, because the file contains the agent's private key.
func checkPrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by other users (mode %04o); run chmod 600 on it", path, info.Mode().Perm())
	}
	return nil
}
