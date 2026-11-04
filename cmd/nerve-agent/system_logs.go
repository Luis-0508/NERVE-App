package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nerve-labs/nerve/internal/agentproto"
)

// logs.read (#92) returns recent log entries through the privileged helper:
// the systemd journal on Linux, the System and Application event logs on
// Windows. The Security log is never read. Core redacts secrets in the result
// before it stores it, as for every command.

// maxLogMessage bounds one message; the policy bounds the whole result.
const maxLogMessage = 2000

type logEntry struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	EventID int       `json:"eventId,omitempty"`
}

func readLogsHandler(ctx context.Context, parameters map[string]any) (map[string]any, error) {
	normalized, err := agentproto.LogParameters(parameters)
	if err != nil {
		return nil, &helperError{"invalid_parameters", err.Error()}
	}
	source, _ := normalized["source"].(string)
	lines, since := normalized["lines"].(int), normalized["sinceMinutes"].(int)
	entries, err := readLogs(ctx, source, lines, time.Duration(since)*time.Minute)
	if err != nil {
		return nil, err
	}
	// Newest first, at most lines entries across all logs.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time.After(entries[j].Time) })
	if len(entries) > lines {
		entries = entries[:lines]
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{"time": entry.Time.UTC().Format(time.RFC3339), "source": entry.Source, "level": entry.Level, "message": entry.Message}
		if entry.EventID != 0 {
			item["eventId"] = entry.EventID
		}
		items = append(items, item)
	}
	if source == "" {
		source = "all"
	}
	return map[string]any{"items": items, "returned": len(items), "source": source, "sinceMinutes": since}, nil
}

func clipMessage(message string) string {
	message = strings.TrimSpace(message)
	if len(message) <= maxLogMessage {
		return message
	}
	cut := maxLogMessage
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + "…"
}

var journalPriorities = []string{"emergency", "alert", "critical", "error", "warning", "notice", "info", "debug"}

// parseJournal reads `journalctl --output=json` lines.
func parseJournal(output []byte) []logEntry {
	var entries []logEntry
	for _, line := range bytes.Split(output, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(line, &fields) != nil {
			continue
		}
		text := func(key string) string {
			var value string
			if json.Unmarshal(fields[key], &value) == nil {
				return value
			}
			// journald encodes non-UTF-8 fields as byte arrays.
			var raw []byte
			var numbers []int
			if json.Unmarshal(fields[key], &numbers) == nil {
				for _, number := range numbers {
					raw = append(raw, byte(number))
				}
				return strings.ToValidUTF8(string(raw), "�")
			}
			return ""
		}
		micros, err := strconv.ParseInt(text("__REALTIME_TIMESTAMP"), 10, 64)
		if err != nil {
			continue
		}
		source := text("_SYSTEMD_UNIT")
		if source == "" {
			source = text("SYSLOG_IDENTIFIER")
		}
		level := "info"
		if priority, err := strconv.Atoi(text("PRIORITY")); err == nil && priority >= 0 && priority < len(journalPriorities) {
			level = journalPriorities[priority]
		}
		entries = append(entries, logEntry{Time: time.UnixMicro(micros), Source: source, Level: level, Message: clipMessage(text("MESSAGE"))})
	}
	return entries
}

// decodeUTF16 converts `wevtutil /uni:true` output (UTF-16LE with a byte-order
// mark). Without /uni wevtutil writes the console code page, which is not
// UTF-8 on most non-English systems.
func decodeUTF16(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte{0xff, 0xfe})
	units := make([]uint16, len(raw)/2)
	for index := range units {
		units[index] = uint16(raw[2*index]) | uint16(raw[2*index+1])<<8
	}
	return []byte(string(utf16.Decode(units)))
}

var eventLevels = map[int]string{1: "critical", 2: "error", 3: "warning", 4: "info", 5: "verbose"}

// parseEvents reads `wevtutil qe /f:RenderedXml` output: <Event> elements
// without a root element.
func parseEvents(output []byte) ([]logEntry, error) {
	var document struct {
		Events []struct {
			System struct {
				Provider struct {
					Name string `xml:"Name,attr"`
				} `xml:"Provider"`
				EventID     int `xml:"EventID"`
				Level       int `xml:"Level"`
				TimeCreated struct {
					SystemTime string `xml:"SystemTime,attr"`
				} `xml:"TimeCreated"`
			} `xml:"System"`
			Rendering struct {
				Message string `xml:"Message"`
			} `xml:"RenderingInfo"`
		} `xml:"Event"`
	}
	wrapped := append(append([]byte("<Events>"), output...), []byte("</Events>")...)
	if err := xml.Unmarshal(wrapped, &document); err != nil {
		return nil, err
	}
	entries := make([]logEntry, 0, len(document.Events))
	for _, event := range document.Events {
		created, err := time.Parse(time.RFC3339Nano, event.System.TimeCreated.SystemTime)
		if err != nil {
			continue
		}
		level, known := eventLevels[event.System.Level]
		if !known {
			level = "info" // Level 0 is "log always" for classic events.
		}
		entries = append(entries, logEntry{Time: created, Source: event.System.Provider.Name, Level: level,
			Message: clipMessage(event.Rendering.Message), EventID: event.System.EventID})
	}
	return entries, nil
}
