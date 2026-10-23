package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	MigrationsDir string
	AllowedOrigin string
	CookieSecure  bool
	TrustProxy    bool
	SessionTTL    time.Duration
	// CommandResultRetention is how long remote command output is stored.
	CommandResultRetention time.Duration
	LogLevel               slog.Level
	DiscoveryAgentToken    string
	CoreSigningKey         string
	NtopngURL              string
	NtopngUsername         string
	NtopngPassword         string
	NtopngInterfaceID      string
	NtopngTimeout          time.Duration
	TrafficLocalCIDRs      string
	TrafficCaptureMode     string
	SensorToken            string
	SensorAddr             string
	CountryDatabase        string
	CityDatabase           string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:               envOr("NERVE_HTTP_ADDR", ":8080"),
		DatabaseURL:            os.Getenv("NERVE_DATABASE_URL"),
		MigrationsDir:          envOr("NERVE_MIGRATIONS_DIR", "migrations"),
		AllowedOrigin:          envOr("NERVE_ALLOWED_ORIGIN", "http://localhost:3000"),
		SessionTTL:             24 * time.Hour,
		CommandResultRetention: 30 * 24 * time.Hour,
		LogLevel:               slog.LevelInfo,
		DiscoveryAgentToken:    os.Getenv("NERVE_DISCOVERY_AGENT_TOKEN"),
		CoreSigningKey:         os.Getenv("NERVE_CORE_SIGNING_KEY"),
		NtopngURL:              os.Getenv("NERVE_NTOPNG_URL"),
		NtopngUsername:         os.Getenv("NERVE_NTOPNG_USERNAME"),
		NtopngPassword:         os.Getenv("NERVE_NTOPNG_PASSWORD"),
		NtopngInterfaceID:      envOr("NERVE_NTOPNG_INTERFACE_ID", "0"),
		NtopngTimeout:          5 * time.Second,
		TrafficLocalCIDRs:      os.Getenv("NERVE_TRAFFIC_LOCAL_CIDRS"),
		TrafficCaptureMode:     os.Getenv("NERVE_TRAFFIC_CAPTURE_MODE"),
		SensorToken:            os.Getenv("NERVE_SENSOR_TOKEN"),
		SensorAddr:             os.Getenv("NERVE_SENSOR_ADDR"),
		CountryDatabase:        envOr("NERVE_COUNTRY_DATABASE", "/app/data/dbip-country-lite.csv.gz"),
		CityDatabase:           envOr("NERVE_CITY_DATABASE", "/app/data/dbip-city-lite.mmdb"),
	}

	if value := os.Getenv("NERVE_COOKIE_SECURE"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse NERVE_COOKIE_SECURE: %w", err)
		}
		cfg.CookieSecure = parsed
	}
	if value := os.Getenv("NERVE_TRUST_PROXY"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse NERVE_TRUST_PROXY: %w", err)
		}
		cfg.TrustProxy = parsed
	}
	if value := os.Getenv("NERVE_SESSION_TTL"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("NERVE_SESSION_TTL must be a positive duration")
		}
		cfg.SessionTTL = parsed
	}
	if value := os.Getenv("NERVE_COMMAND_RESULT_RETENTION"); value != "" {
		parsed, err := parseRetention(value)
		if err != nil {
			return Config{}, err
		}
		cfg.CommandResultRetention = parsed
	}
	if value := os.Getenv("NERVE_LOG_LEVEL"); value != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(value)); err != nil {
			return Config{}, fmt.Errorf("parse NERVE_LOG_LEVEL: %w", err)
		}
	}
	if value := os.Getenv("NERVE_NTOPNG_TIMEOUT"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("NERVE_NTOPNG_TIMEOUT must be a positive duration")
		}
		cfg.NtopngTimeout = parsed
	}
	for _, raw := range strings.Split(cfg.TrafficLocalCIDRs, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, err := netip.ParsePrefix(strings.TrimSpace(raw)); err != nil {
			return Config{}, fmt.Errorf("NERVE_TRAFFIC_LOCAL_CIDRS contains an invalid CIDR")
		}
	}
	if strings.TrimSpace(cfg.CoreSigningKey) != "" {
		if _, err := agentproto.ParseCoreSigningKey(cfg.CoreSigningKey); err != nil {
			return Config{}, err
		}
	}
	switch cfg.TrafficCaptureMode {
	case "", "sensor-required", "external", "linux-interface", "windows-native":
	default:
		return Config{}, fmt.Errorf("NERVE_TRAFFIC_CAPTURE_MODE must be sensor-required, external, linux-interface or windows-native")
	}
	if cfg.NtopngURL != "" && cfg.TrafficCaptureMode == "sensor-required" {
		cfg.TrafficCaptureMode = "external"
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// parseRetention accepts a Go duration ("720h") or whole days ("30d"), from
// one hour up to ten years.
func parseRetention(value string) (time.Duration, error) {
	var parsed time.Duration
	var err error
	if days, ok := strings.CutSuffix(strings.TrimSpace(value), "d"); ok {
		var count int
		count, err = strconv.Atoi(days)
		parsed = time.Duration(count) * 24 * time.Hour
	} else {
		parsed, err = time.ParseDuration(strings.TrimSpace(value))
	}
	if err != nil || parsed < time.Hour || parsed > 10*365*24*time.Hour {
		return 0, fmt.Errorf("NERVE_COMMAND_RESULT_RETENTION must be a duration between 1h and 3650d")
	}
	return parsed, nil
}
