//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

func osVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	product, _, _ := key.GetStringValue("ProductName")
	display, _, _ := key.GetStringValue("DisplayVersion")
	build, _, _ := key.GetStringValue("CurrentBuild")
	// ProductName still says "Windows 10" on Windows 11; build 22000+ is Windows 11.
	if strings.HasPrefix(product, "Windows 10") && build >= "22000" && len(build) == 5 {
		product = "Windows 11" + strings.TrimPrefix(product, "Windows 10")
	}
	parts := []string{product}
	if display != "" {
		parts = append(parts, display)
	}
	if build != "" {
		parts = append(parts, "(build "+build+")")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func machineID() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return value
}
