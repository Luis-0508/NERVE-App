//go:build !windows && !linux

package main

import (
	"errors"
	"time"
)

var errServiceUnsupported = errors.New("service installation is supported on Windows and Linux (systemd) only")

func runningAsService() bool { return false }

func runAsService(runOptions) int { return 1 }

func detectRunMode() string { return "foreground" }

func sdNotify(string) {}

func watchdogInterval() time.Duration { return 0 }

func installService(installOptions) error { return errServiceUnsupported }

func uninstallService(string, bool) error { return errServiceUnsupported }

func controlService(string) (string, error) { return "", errServiceUnsupported }
