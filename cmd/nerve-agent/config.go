package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	configFileName = "config.json"
	// caFileName is the service's private copy of an additional Core CA.
	caFileName = "ca.pem"
)

// agentConfig is the persistent, host-owned agent configuration. It lives next
// to the identity in the protected state directory, so a service does not
// depend on environment variables. Environment variables still override it.
type agentConfig struct {
	APIURL               string   `json:"apiUrl,omitempty"`
	CAFile               string   `json:"caFile,omitempty"`
	DisabledCapabilities []string `json:"disabledCapabilities,omitempty"`
	LogLevel             string   `json:"logLevel,omitempty"`
	// CredentialMaxAgeDays makes a running agent rotate its key once the
	// credential is older than this many days. 0 (the default) turns it off.
	CredentialMaxAgeDays int `json:"credentialMaxAgeDays,omitempty"`
}

// settings are the effective values after applying environment overrides.
type settings struct {
	StateDir             string
	APIURL               string
	CAFile               string
	DisabledCapabilities []string
	LogLevel             slog.Level
	CredentialMaxAge     time.Duration
}

func loadConfig(dir string) (agentConfig, error) {
	path := filepath.Join(dir, configFileName)
	if err := checkPrivateConfig(path); err != nil {
		return agentConfig{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return agentConfig{}, err
	}
	var value agentConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return agentConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return value, nil
}

func saveConfig(dir string, value agentConfig) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(dir, configFileName, encoded)
}

// resolveSettings merges the config file with environment overrides. A missing
// config file is not an error: foreground agents may use only the environment.
// The identity's recorded URL is the last fallback for the API URL.
func resolveSettings(stateDir string, id *identity) (settings, error) {
	config, err := loadConfig(stateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return settings{}, err
	}
	result := settings{StateDir: stateDir, APIURL: config.APIURL, CAFile: config.CAFile, LogLevel: slog.LevelInfo}
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_API_URL")); value != "" {
		result.APIURL = value
	}
	if result.APIURL == "" && id != nil {
		result.APIURL = id.APIURL
	}
	result.APIURL = strings.TrimRight(strings.TrimSpace(result.APIURL), "/")
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_CA_FILE")); value != "" {
		result.CAFile = value
	}
	result.DisabledCapabilities = append(append([]string{}, config.DisabledCapabilities...), splitList(os.Getenv("NERVE_AGENT_DISABLED_CAPABILITIES"))...)
	levelName := config.LogLevel
	if value := strings.TrimSpace(os.Getenv("NERVE_AGENT_LOG_LEVEL")); value != "" {
		levelName = value
	}
	if levelName != "" {
		if err := result.LogLevel.UnmarshalText([]byte(levelName)); err != nil {
			return settings{}, fmt.Errorf("invalid log level %q", levelName)
		}
	}
	if config.CredentialMaxAgeDays < 0 || config.CredentialMaxAgeDays > 3650 {
		return settings{}, fmt.Errorf("credentialMaxAgeDays must be between 0 (off) and 3650")
	}
	result.CredentialMaxAge = time.Duration(config.CredentialMaxAgeDays) * 24 * time.Hour
	if result.APIURL != "" {
		if err := validateAPIURL(result.APIURL); err != nil {
			return settings{}, err
		}
	}
	return result, nil
}

// validateAPIURL accepts only absolute http(s) URLs without credentials.
func validateAPIURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("the Core API URL %q must be an absolute http(s) URL such as https://nerve.example/api/v1", value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("the Core API URL must not contain credentials, a query or a fragment")
	}
	return nil
}

// newTransport validates server certificates against the system roots plus an
// optional private CA (for self-hosted Core behind an internal PKI). TLS 1.2
// is the minimum. Certificate verification is never disabled.
func newTransport(caFile string) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s contains no PEM certificates", caFile)
		}
		config.RootCAs = pool
	}
	transport.TLSClientConfig = config
	return transport, nil
}

// capabilitiesWithout returns the host's capabilities minus disabled ones.
func capabilitiesWithout(disabled []string) func() []string {
	off := map[string]bool{}
	for _, name := range disabled {
		off[name] = true
	}
	return func() []string {
		result := []string{}
		for _, name := range supportedCapabilities() {
			if !off[name] {
				result = append(result, name)
			}
		}
		return result
	}
}

// writePrivateFile atomically replaces dir/name in a directory restricted to
// the agent's account (and administrators).
func writePrivateFile(dir, name string, content []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent state directory: %w", err)
	}
	if err := securePrivateDir(dir); err != nil {
		return fmt.Errorf("restrict agent state directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := preparePrivateFile(temporary, dir); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(dir, name))
}
