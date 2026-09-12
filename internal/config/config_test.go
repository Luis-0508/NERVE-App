package config

import "testing"

func TestTrafficEnvironment(t *testing.T) {
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
