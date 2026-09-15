package config

import "testing"

func TestCaptureModeCompatibility(t *testing.T) {
	t.Setenv("NERVE_TRAFFIC_CAPTURE_MODE", "sensor-required")
	t.Setenv("NERVE_NTOPNG_URL", "http://sensor:3001")
	t.Setenv("NERVE_NTOPNG_INTERFACE_ID", "auto")
	cfg, err := Load()
	if err != nil || cfg.TrafficCaptureMode != "external" || cfg.NtopngInterfaceID != "auto" {
		t.Fatal("legacy source must remain usable")
	}
	t.Setenv("NERVE_TRAFFIC_CAPTURE_MODE", "full-lan")
	if _, err := Load(); err == nil {
		t.Fatal("unrecognized mode accepted")
	}
}
