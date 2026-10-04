//go:build !linux && !windows

package main

func osVersion() string { return "" }

func machineID() string { return "" }
