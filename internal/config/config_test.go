package config

import (
	"testing"
	"time"
)

func TestNativeSensorEnvironment(t *testing.T) {
	t.Setenv("NERVE_TRAFFIC_CAPTURE_MODE", "windows-native")
	t.Setenv("NERVE_SENSOR_TOKEN", "sensor-test-only-token")
	t.Setenv("NERVE_SENSOR_ADDR", ":8091")
	cfg, err := Load()
	if err != nil || cfg.TrafficCaptureMode != "windows-native" || cfg.SensorToken != "sensor-test-only-token" || cfg.SensorAddr != ":8091" {
		t.Fatal("native sensor configuration not loaded")
	}
	t.Setenv("NERVE_TRAFFIC_CAPTURE_MODE", "external")
	cfg, err = Load()
	if err != nil || cfg.TrafficCaptureMode != "external" {
		t.Fatal("explicit external source was replaced")
	}
}

func TestTrafficEnvironment(t *testing.T) {
	t.Setenv("NERVE_TRAFFIC_CAPTURE_MODE", "linux-interface")
	t.Setenv("NERVE_NTOPNG_URL", "http://ntopng:3000")
	t.Setenv("NERVE_NTOPNG_USERNAME", "reader")
	t.Setenv("NERVE_NTOPNG_PASSWORD", "example-only")
	t.Setenv("NERVE_NTOPNG_INTERFACE_ID", "3")
	t.Setenv("NERVE_NTOPNG_TIMEOUT", "8s")
	t.Setenv("NERVE_TRAFFIC_LOCAL_CIDRS", "100.64.0.0/10,2001:db8::/32")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TrafficCaptureMode != "linux-interface" {
		t.Fatal("capture mode not loaded")
	}
	if cfg.NtopngURL != "http://ntopng:3000" || cfg.NtopngUsername != "reader" || cfg.NtopngPassword != "example-only" || cfg.NtopngInterfaceID != "3" || cfg.NtopngTimeout.Seconds() != 8 {
		t.Fatal("traffic environment not loaded")
	}
	t.Setenv("NERVE_TRAFFIC_LOCAL_CIDRS", "invalid")
	if _, err := Load(); err == nil {
		t.Fatal("invalid CIDR silently ignored")
	}
	t.Setenv("NERVE_TRAFFIC_LOCAL_CIDRS", "")
	t.Setenv("NERVE_NTOPNG_TIMEOUT", "-1s")
	if _, err := Load(); err == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestCommandResultRetention(t *testing.T) {
	cfg, err := Load()
	if err != nil || cfg.CommandResultRetention != 30*24*time.Hour {
		t.Fatalf("default retention = %v (err %v), want 30 days", cfg.CommandResultRetention, err)
	}
	for value, want := range map[string]time.Duration{"7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour, " 90d ": 90 * 24 * time.Hour} {
		t.Setenv("NERVE_COMMAND_RESULT_RETENTION", value)
		if cfg, err := Load(); err != nil || cfg.CommandResultRetention != want {
			t.Errorf("%q: retention = %v (err %v), want %v", value, cfg.CommandResultRetention, err, want)
		}
	}
	for _, value := range []string{"0", "30m", "-1d", "d", "forever", "4000d"} {
		t.Setenv("NERVE_COMMAND_RESULT_RETENTION", value)
		if _, err := Load(); err == nil {
			t.Errorf("%q was accepted", value)
		}
	}
}

func TestDiscoveryLegacyTokenDefaultsOn(t *testing.T) {
	t.Setenv("NERVE_DISCOVERY_LEGACY_TOKEN", "")
	cfg, err := Load()
	if err != nil || !cfg.DiscoveryLegacyToken {
		t.Fatal("the legacy discovery token must stay enabled by default in this release")
	}
	t.Setenv("NERVE_DISCOVERY_LEGACY_TOKEN", "false")
	if cfg, err = Load(); err != nil || cfg.DiscoveryLegacyToken {
		t.Fatal("NERVE_DISCOVERY_LEGACY_TOKEN=false did not disable the legacy path")
	}
	t.Setenv("NERVE_DISCOVERY_LEGACY_TOKEN", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("an invalid NERVE_DISCOVERY_LEGACY_TOKEN was accepted")
	}
}
