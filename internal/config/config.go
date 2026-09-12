package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr            string
	DatabaseURL         string
	MigrationsDir       string
	AllowedOrigin       string
	CookieSecure        bool
	TrustProxy          bool
	SessionTTL          time.Duration
	LogLevel            slog.Level
	DiscoveryAgentToken string
	NtopngURL           string
	NtopngUsername      string
	NtopngPassword      string
	NtopngInterfaceID   string
	NtopngTimeout       time.Duration
	TrafficLocalCIDRs   string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            envOr("NERVE_HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("NERVE_DATABASE_URL"),
		MigrationsDir:       envOr("NERVE_MIGRATIONS_DIR", "migrations"),
		AllowedOrigin:       envOr("NERVE_ALLOWED_ORIGIN", "http://localhost:3000"),
		SessionTTL:          24 * time.Hour,
		LogLevel:            slog.LevelInfo,
		DiscoveryAgentToken: os.Getenv("NERVE_DISCOVERY_AGENT_TOKEN"),
		NtopngURL:           os.Getenv("NERVE_NTOPNG_URL"),
		NtopngUsername:      os.Getenv("NERVE_NTOPNG_USERNAME"),
		NtopngPassword:      os.Getenv("NERVE_NTOPNG_PASSWORD"),
		NtopngInterfaceID:   envOr("NERVE_NTOPNG_INTERFACE_ID", "0"),
		NtopngTimeout:       5 * time.Second,
		TrafficLocalCIDRs:   os.Getenv("NERVE_TRAFFIC_LOCAL_CIDRS"),
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
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
