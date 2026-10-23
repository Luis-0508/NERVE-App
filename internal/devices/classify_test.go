package devices

import "testing"

func TestClassify(t *testing.T) {
	for _, test := range []struct {
		name           string
		inputs         ClassificationInputs
		classification string
		confidence     int
	}{
		{"windows server edition", ClassificationInputs{OperatingSystem: "Windows Server 2022 Standard 21H2 (build 20348)"}, "server", 75},
		{"windows client edition", ClassificationInputs{OperatingSystem: "Windows 11 Pro 24H2 (build 26100)"}, "workstation", 60},
		{"virtualization vendor", ClassificationInputs{Vendor: "VMware, Inc."}, "virtual_machine", 70},
		{"camera vendor", ClassificationInputs{Vendor: "Hangzhou Hikvision Digital Technology Co.,Ltd."}, "camera", 55},
		{"camera vendor, registry typo", ClassificationInputs{Vendor: "ZHEJIANG DAHUA TECHNOLOGYCO.,LTD"}, "camera", 55},
		{"storage vendor", ClassificationInputs{Vendor: "Synology Incorporated"}, "storage", 55},
		{"printer vendor, any case", ClassificationInputs{Vendor: "Brother industries, LTD."}, "printer", 50},
		{"media vendor", ClassificationInputs{Vendor: "Sonos, Inc."}, "media", 50},
		{"router vendor", ClassificationInputs{Vendor: "AVM Audiovisuelles Marketing und Computersysteme GmbH"}, "router", 45},
		{"iot vendor", ClassificationInputs{Vendor: "Espressif Inc."}, "iot", 45},
		{"printing port", ClassificationInputs{OpenTCPPorts: []int{80, 9100}}, "printer", 45},
		{"proxmox port", ClassificationInputs{OpenTCPPorts: []int{22, 8006}}, "hypervisor", 65},
		{"rtsp port", ClassificationInputs{OpenTCPPorts: []int{554}}, "camera", 40},
		// Conflicts: the most confident rule wins.
		{"windows vm: vendor beats client os", ClassificationInputs{Vendor: "VMware, Inc.", OperatingSystem: "Windows 11 Pro"}, "virtual_machine", 70},
		{"windows server vm: server os beats vendor", ClassificationInputs{Vendor: "VMware, Inc.", OperatingSystem: "Windows Server 2025 Datacenter"}, "server", 75},
		{"printer vendor beats rtsp port", ClassificationInputs{Vendor: "Seiko Epson Corporation", OpenTCPPorts: []int{554}}, "printer", 50},
		// Equal confidence: the earlier rule wins.
		{"tie keeps table order", ClassificationInputs{Vendor: "Espressif Inc.", OpenTCPPorts: []int{9100}}, "iot", 45},
	} {
		t.Run(test.name, func(t *testing.T) {
			suggestion, ok := Classify(test.inputs)
			if !ok || suggestion.Classification != test.classification || suggestion.Confidence != test.confidence ||
				suggestion.Source != SourceHeuristic || suggestion.Reason == "" {
				t.Fatalf("Classify(%+v) = %+v, %v; want %s at %d with a reason", test.inputs, suggestion, ok, test.classification, test.confidence)
			}
		})
	}
}

func TestClassifyLeavesUnknown(t *testing.T) {
	for name, inputs := range map[string]ClassificationInputs{
		"no evidence":           {},
		"linux":                 {OperatingSystem: "Ubuntu 24.04.1 LTS"},
		"multi-purpose vendor":  {Vendor: "Cisco Systems, Inc"},
		"vendor name substring": {Vendor: "Brotherhood Networks"},
		"similar vendor name":   {Vendor: "Brother, Brother & Sons ApS"},
		"other avm":             {Vendor: "AVM ELECTRONICS PTE LTD"},
		"kyocera phones":        {Vendor: "Kyocera Wireless Corp."},
		"unrelated ports":       {OpenTCPPorts: []int{22, 80, 443}},
		"server word in client": {OperatingSystem: "Ubuntu Server 24.04"},
	} {
		if suggestion, ok := Classify(inputs); ok {
			t.Errorf("%s: Classify(%+v) = %+v, want no suggestion", name, inputs, suggestion)
		}
	}
}

func TestClassificationRulesUseVocabulary(t *testing.T) {
	for _, rule := range classificationRules {
		if !validClassification(rule.classification) || rule.reason == "" || rule.confidence >= ConfidenceAgent {
			t.Errorf("rule %+v: classification must be in the vocabulary, have a reason and stay below agent confidence", rule)
		}
	}
}
