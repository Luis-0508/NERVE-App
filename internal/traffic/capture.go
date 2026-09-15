package traffic

// Capture describes deployment configuration, not a measured coverage guarantee.
// It intentionally excludes the sensor URL and all credentials.
type Capture struct {
	Mode          string `json:"mode"`
	InterfaceID   string `json:"interfaceId"`
	InterfaceName string `json:"interfaceName,omitempty"`
	Visibility    string `json:"visibility"`
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
	return Capture{Mode: mode, InterfaceID: id, Visibility: visibility}
}
