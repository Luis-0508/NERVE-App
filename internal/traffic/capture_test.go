package traffic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCaptureCoverageAndRedaction(t *testing.T) {
	for _, tc := range []struct{ mode, url, want string }{
		{"", "", "sensor-required"}, {"", "http://private-sensor", "external"},
		{"linux-interface", "http://private-sensor", "linux-interface"},
	} {
		capture := CaptureInfo(tc.mode, tc.url, "auto")
		if capture.Mode != tc.want || capture.Visibility == "" {
			t.Fatal("missing coverage information")
		}
		data, _ := json.Marshal(capture)
		if strings.Contains(string(data), "private-sensor") {
			t.Fatal("sensor address leaked")
		}
	}
}
