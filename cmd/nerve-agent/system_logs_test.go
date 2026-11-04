package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseJournal(t *testing.T) {
	output := []byte(`{"__REALTIME_TIMESTAMP":"1762200000000000","_SYSTEMD_UNIT":"nginx.service","PRIORITY":"3","MESSAGE":"upstream failed"}
{"__REALTIME_TIMESTAMP":"1762200001000000","SYSLOG_IDENTIFIER":"kernel","PRIORITY":"6","MESSAGE":[104,105,255]}
not json
{"MESSAGE":"no timestamp"}
`)
	entries := parseJournal(output)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].Source != "nginx.service" || entries[0].Level != "error" || entries[0].Message != "upstream failed" ||
		!entries[0].Time.Equal(time.UnixMicro(1762200000000000)) {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[1].Source != "kernel" || entries[1].Level != "info" || entries[1].Message != "hi�" {
		t.Fatalf("byte-array entry = %+v", entries[1])
	}
}

func TestParseEvents(t *testing.T) {
	output := []byte(`<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Service Control Manager'/>` +
		`<EventID Qualifiers='16384'>7036</EventID><Level>4</Level><TimeCreated SystemTime='2026-11-04T08:15:30.1234567Z'/></System>` +
		`<RenderingInfo Culture='en-US'><Message>The Print Spooler service entered the stopped state.</Message></RenderingInfo></Event>` +
		`<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='App'/><EventID>1</EventID><Level>2</Level>` +
		`<TimeCreated SystemTime='2026-11-04T08:16:00Z'/></System></Event>`)
	entries, err := parseEvents(output)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if entries[0].Source != "Service Control Manager" || entries[0].EventID != 7036 || entries[0].Level != "info" ||
		!strings.Contains(entries[0].Message, "Print Spooler") {
		t.Fatalf("first event = %+v", entries[0])
	}
	if entries[1].Level != "error" || entries[1].Message != "" {
		t.Fatalf("second event = %+v", entries[1])
	}
	if entries, err := parseEvents(nil); err != nil || len(entries) != 0 {
		t.Fatalf("empty output: %v %v", entries, err)
	}
}

func TestDecodeUTF16(t *testing.T) {
	raw := []byte{0xff, 0xfe, '<', 0, 0xe4, 0, 0x3d, 0xd8, 0x00, 0xde, '>', 0} // "<ä😀>"
	if got := string(decodeUTF16(raw)); got != "<ä😀>" {
		t.Fatalf("decoded %q", got)
	}
}

func TestClipMessage(t *testing.T) {
	long := strings.Repeat("ä", maxLogMessage)
	clipped := clipMessage(long)
	if len(clipped) > maxLogMessage+len("…") || !strings.HasSuffix(clipped, "…") || !strings.HasPrefix(clipped, "ä") {
		t.Fatalf("clipped to %d bytes", len(clipped))
	}
}
