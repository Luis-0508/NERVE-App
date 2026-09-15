package traffic

// Capture describes deployment configuration, not a measured coverage guarantee.
// It intentionally excludes the sensor URL and all credentials.
type Capture struct {
	Mode          string `json:"mode"`
	InterfaceID   string `json:"interfaceId"`
	InterfaceName string `json:"interfaceName,omitempty"`
	Visibility    string `json:"visibility"`
	EventsLost    int64  `json:"eventsLost,omitempty"`
	BackendStage  string `json:"backendStage,omitempty"`
	BackendError  int    `json:"backendError,omitempty"`
}

func CaptureInfo(mode, url, id string) Capture {
	if mode == "" {
		mode = "external"
		if url == "" {
			mode = "sensor-required"
		}
	}
	visibility := "Coverage is not verified. Only traffic delivered to the selected sensor interface is visible. LAN monitoring requires a suitable observation point, SPAN or TAP."
	if mode == "sensor-required" {
		visibility = "No packet capture is configured. Docker Desktop cannot capture the Windows network adapter. Optional: connect a capture sensor using traffic-sensor-setup.bat."
	}
	if mode == "linux-interface" {
		visibility = "Linux interface capture: host traffic and packets delivered to this NIC only. Other LAN devices require SPAN/TAP or a routing observation point. Coverage is not verified."
	}
	if mode == "windows-native" {
		visibility = "Windows host capture: TCP/UDP on the selected adapter's local addresses. Host traffic only; LAN-wide monitoring requires gateway/SPAN/TAP or additional sensors. ETW byte counts are transport events, not a wire-level packet accounting guarantee."
	}
	return Capture{Mode: mode, InterfaceID: id, Visibility: visibility}
}
