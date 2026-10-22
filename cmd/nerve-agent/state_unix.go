//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
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

// preparePrivateFile makes a new state file owner-only. When root writes into
// a directory owned by the service account (for example `enroll --force` on an
// installed agent), the file is handed to that account so the service can
// still read it.
func preparePrivateFile(file *os.File, dir string) error {
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok && os.Geteuid() == 0 && stat.Uid != 0 {
		return file.Chown(int(stat.Uid), int(stat.Gid))
	}
	return nil
}

// checkPrivateConfig refuses configuration that other accounts could modify,
// because it selects the Core URL and the trusted CA.
func checkPrivateConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users (mode %04o); run chmod 600 on it", path, info.Mode().Perm())
	}
	return nil
}
