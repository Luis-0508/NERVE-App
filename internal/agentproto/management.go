package agentproto

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Parameter rules for the system-management commands (#92). Core applies them
// before a command is stored and signed; the privileged helper applies them
// again, together with its platform's protection list, before it acts.

// serviceNamePattern accepts systemd unit names and Windows service key names.
// It excludes path separators, whitespace and a leading dash, so a name can
// never be read as an option.
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:+-]{0,254}$`)

// ServiceParameters validates {"name": "<service>"}.
func ServiceParameters(parameters map[string]any) (map[string]any, error) {
	if err := onlyKeys(parameters, "name"); err != nil {
		return nil, err
	}
	name, _ := parameters["name"].(string)
	if !serviceNamePattern.MatchString(name) {
		return nil, errors.New("name must be a service name of letters, digits and . _ @ : + - (at most 255 characters)")
	}
	return map[string]any{"name": name}, nil
}

// ProcessParameters validates {"pid": <number>, "name": "<process name>"}. The
// name must match the running process, so a reused PID never ends another
// process.
func ProcessParameters(parameters map[string]any) (map[string]any, error) {
	if err := onlyKeys(parameters, "pid", "name"); err != nil {
		return nil, err
	}
	pid, ok := parameters["pid"].(float64)
	if !ok {
		if value, isInt := parameters["pid"].(int); isInt {
			pid, ok = float64(value), true
		}
	}
	if !ok || pid != math.Trunc(pid) || pid < 2 || pid > math.MaxUint32 {
		return nil, errors.New("pid must be an integer greater than 1")
	}
	name, _ := parameters["name"].(string)
	if name == "" || len(name) > 255 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return nil, errors.New("name must be the process name as listed by process.list, without a path")
	}
	return map[string]any{"pid": int(pid), "name": name}, nil
}

// logSourcePattern accepts systemd unit names and Windows event provider names
// (which may contain spaces). Quotes, brackets and slashes are excluded, so a
// source is always a plain literal in a journalctl argument or an event query.
var logSourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._@:+-]{0,254}$`)

// LogParameters validates {"source": optional unit or provider, "lines":
// 1-500, "sinceMinutes": 1-10080} and fills in the defaults (100 lines, the
// last hour), so the approval shows exactly what will be read.
func LogParameters(parameters map[string]any) (map[string]any, error) {
	if err := onlyKeys(parameters, "source", "lines", "sinceMinutes"); err != nil {
		return nil, err
	}
	normalized := map[string]any{"lines": 100, "sinceMinutes": 60}
	if value, present := parameters["source"]; present {
		source, _ := value.(string)
		if !logSourcePattern.MatchString(source) || strings.TrimSpace(source) != source {
			return nil, errors.New("source must be a service or event source name of letters, digits, spaces and . _ @ : + -")
		}
		normalized["source"] = source
	}
	for key, limit := range map[string]float64{"lines": 500, "sinceMinutes": 10080} {
		value, present := parameters[key]
		if !present {
			continue
		}
		number, ok := value.(float64)
		if !ok {
			if integer, isInt := value.(int); isInt {
				number, ok = float64(integer), true
			}
		}
		if !ok || number != math.Trunc(number) || number < 1 || number > limit {
			return nil, fmt.Errorf("%s must be an integer between 1 and %d", key, int(limit))
		}
		normalized[key] = int(number)
	}
	return normalized, nil
}

// NoParameters accepts only an empty parameter set.
func NoParameters(parameters map[string]any) (map[string]any, error) {
	if len(parameters) != 0 {
		return nil, errors.New("this command takes no parameters")
	}
	return map[string]any{}, nil
}

func onlyKeys(parameters map[string]any, allowed ...string) error {
	for key := range parameters {
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if !known {
			return fmt.Errorf("unknown parameter %q", key)
		}
	}
	return nil
}
