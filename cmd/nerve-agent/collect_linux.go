//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

func platformCapabilities() []string {
	capabilities := []string{}
	if _, err := os.Stat("/proc/stat"); err == nil {
		capabilities = append(capabilities, agentproto.CapabilitySystemInfo, agentproto.CapabilitySystemMetrics, agentproto.CapabilityProcessList)
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		if _, err := os.Stat("/run/systemd/system"); err == nil {
			capabilities = append(capabilities, agentproto.CapabilityServiceList)
		}
	}
	if softwareSource() != "" {
		capabilities = append(capabilities, agentproto.CapabilitySoftwareList)
	}
	return capabilities
}

func kernelVersion() string {
	raw, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(raw))
}

func systemUptime() time.Duration {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

func cpuTimes() (idle, total uint64, err error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for index, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			// guest and guest_nice are already included in user and nice.
			if index >= 8 {
				break
			}
			total += value
			if index == 3 || index == 4 { // idle, iowait
				idle += value
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("no aggregate cpu line in /proc/stat")
}

func cpuUsage(ctx context.Context, sample time.Duration) (float64, error) {
	idle1, total1, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	if !sleepContext(ctx, sample) {
		return 0, ctx.Err()
	}
	idle2, total2, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	if total2 <= total1 {
		return 0, nil
	}
	return 100 * (1 - float64(idle2-idle1)/float64(total2-total1)), nil
}

func memoryUsage() (map[string]any, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]uint64{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			if value, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				values[strings.TrimSuffix(fields[0], ":")] = value * 1024
			}
		}
	}
	total, available := values["MemTotal"], values["MemAvailable"]
	if total == 0 {
		return nil, fmt.Errorf("MemTotal missing from /proc/meminfo")
	}
	used := total - available
	return map[string]any{"totalBytes": total, "availableBytes": available, "usedBytes": used, "usagePercent": percent(used, total)}, nil
}

// mountEscapes decodes the octal escapes the kernel uses in /proc/mounts.
var mountEscapes = strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

var realFileSystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "vfat": true, "exfat": true,
	"ntfs": true, "ntfs3": true, "f2fs": true, "jfs": true, "reiserfs": true, "bcachefs": true,
}

func diskUsage() ([]map[string]any, error) {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	disks := []map[string]any{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !realFileSystems[fields[2]] || seen[fields[0]] {
			continue
		}
		mount := mountEscapes.Replace(fields[1])
		var stat syscall.Statfs_t
		if syscall.Statfs(mount, &stat) != nil || stat.Blocks == 0 {
			continue
		}
		seen[fields[0]] = true
		size := uint64(stat.Bsize)
		total, free := stat.Blocks*size, stat.Bavail*size
		used := total - stat.Bfree*size
		disks = append(disks, map[string]any{"mountPoint": mount, "fileSystem": fields[2], "totalBytes": total,
			"freeBytes": free, "usedBytes": used, "usagePercent": percent(used, total)})
		if len(disks) >= 64 {
			break
		}
	}
	return disks, nil
}

func listProcesses(ctx context.Context) ([]map[string]any, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pageSize := uint64(os.Getpagesize())
	users := map[string]string{}
	processes := []map[string]any{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue // The process exited.
		}
		open, closing := bytes.IndexByte(raw, '('), bytes.LastIndexByte(raw, ')')
		if open < 0 || closing < open {
			continue
		}
		fields := strings.Fields(string(raw[closing+1:]))
		if len(fields) < 22 {
			continue
		}
		ppid, _ := strconv.ParseUint(fields[1], 10, 32)
		threads, _ := strconv.ParseUint(fields[17], 10, 32)
		rss, _ := strconv.ParseUint(fields[21], 10, 64)
		process := map[string]any{"pid": uint32(pid), "parentPid": uint32(ppid), "name": string(raw[open+1 : closing]),
			"state": fields[0], "threads": uint32(threads), "memoryBytes": rss * pageSize}
		if uid := processUID(entry.Name()); uid != "" {
			name, cached := users[uid]
			if !cached {
				if account, err := user.LookupId(uid); err == nil {
					name = account.Username
				}
				users[uid] = name
			}
			if name == "" {
				name = uid
			}
			process["user"] = name
		}
		processes = append(processes, process)
	}
	return processes, nil
}

func processUID(pid string) string {
	file, err := os.Open(filepath.Join("/proc", pid, "status"))
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if value, found := strings.CutPrefix(scanner.Text(), "Uid:"); found {
			if fields := strings.Fields(value); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return ""
}

func runQuery(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = []string{"LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}

func listServices(ctx context.Context) (string, []map[string]any, error) {
	output, err := runQuery(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--no-pager", "--plain")
	if err != nil {
		return "", nil, err
	}
	services := []map[string]any{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		services = append(services, map[string]any{"name": strings.TrimSuffix(fields[0], ".service"), "unit": fields[0],
			"loadState": fields[1], "state": fields[2], "subState": fields[3], "displayName": strings.Join(fields[4:], " ")})
	}
	return "systemd", services, nil
}

func softwareSource() string {
	if _, err := exec.LookPath("dpkg-query"); err == nil {
		return "dpkg"
	}
	if _, err := exec.LookPath("rpm"); err == nil {
		return "rpm"
	}
	return ""
}

func listSoftware(ctx context.Context) (string, []map[string]any, error) {
	source := softwareSource()
	var (
		output []byte
		err    error
	)
	switch source {
	case "dpkg":
		output, err = runQuery(ctx, "dpkg-query", "-W", "-f=${binary:Package}\t${Version}\t${Architecture}\t${db:Status-Abbrev}\n")
	case "rpm":
		output, err = runQuery(ctx, "rpm", "-qa", "--queryformat", "%{NAME}\t%{VERSION}-%{RELEASE}\t%{ARCH}\tii\n")
	default:
		return "", nil, errUnsupported
	}
	if err != nil {
		return "", nil, err
	}
	packages := []map[string]any{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 4 || !strings.HasPrefix(strings.TrimSpace(fields[3]), "ii") {
			continue
		}
		packages = append(packages, map[string]any{"name": fields[0], "version": fields[1], "architecture": fields[2]})
	}
	return source, packages, nil
}
