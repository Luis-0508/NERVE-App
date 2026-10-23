// Package redact removes common secrets from command output before it is
// stored. The agent applies it before upload and Core applies it again on
// receipt, so the rules must be idempotent: redacting redacted text changes
// nothing and counts nothing.
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Kinds of secrets, used in the `[redacted:<kind>]` marker and as count keys.
const (
	KindPrivateKey     = "private_key"
	KindURLCredentials = "url_credentials"
	KindBearerToken    = "bearer_token"
	KindAWSAccessKey   = "aws_access_key"
	KindPassword       = "password"
)

// Kinds lists every kind a count may name.
var Kinds = []string{KindPrivateKey, KindURLCredentials, KindBearerToken, KindAWSAccessKey, KindPassword}

const markerPrefix = "[redacted:"

func marker(kind string) string { return markerPrefix + kind + "]" }

// A rule replaces submatch `secret` of each match; the text around it (a key
// name, a scheme and user name) stays readable.
type rule struct {
	kind    string
	pattern *regexp.Regexp
	secret  int
}

// Rules run in order; earlier rules consume text later rules would also match.
var rules = []rule{
	// A truncated block without its END line is still removed to the end.
	{KindPrivateKey, regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|[\s\S]*$)`), 0},
	{KindURLCredentials, regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@]+:([^/\s@]+)@`), 1},
	{KindBearerToken, regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{8,}=*)`), 1},
	{KindAWSAccessKey, regexp.MustCompile(`\b((?:AKIA|ASIA)[A-Z0-9]{16})\b`), 1},
	{KindPassword, regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|client_secret|aws_secret_access_key|api[_-]?key|access[_-]?token|auth[_-]?token|token)` +
		`\s*[=:]\s*("[^"\n]*"|'[^'\n]*'|[^\s"',;&]+)`), 1},
}

// Counts records how many secrets of each kind were replaced.
type Counts map[string]int

// Add merges other into c.
func (c Counts) Add(other Counts) {
	for kind, count := range other {
		c[kind] += count
	}
}

// Text redacts one string.
func Text(text string) (string, Counts) {
	counts := Counts{}
	for _, rule := range rules {
		text = replace(text, rule, counts)
	}
	return text, counts
}

func replace(text string, rule rule, counts Counts) string {
	matches := rule.pattern.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text
	}
	var out strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[2*rule.secret], match[2*rule.secret+1]
		if start < 0 {
			continue
		}
		secret := strings.Trim(text[start:end], `"'`)
		if secret == "" || strings.HasPrefix(secret, markerPrefix) {
			continue
		}
		out.WriteString(text[last:start])
		out.WriteString(marker(rule.kind))
		last = end
		counts[rule.kind]++
	}
	out.WriteString(text[last:])
	return out.String()
}

// JSON redacts every string value (not key) in a JSON document. A document
// that does not parse is returned unchanged; callers validate JSON separately.
func JSON(document json.RawMessage) (json.RawMessage, Counts) {
	counts := Counts{}
	if len(document) == 0 {
		return document, counts
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return document, counts
	}
	value = walk(value, counts)
	if len(counts) == 0 {
		return document, counts
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return document, Counts{}
	}
	return json.RawMessage(bytes.TrimRight(buffer.Bytes(), "\n")), counts
}

func walk(value any, counts Counts) any {
	switch typed := value.(type) {
	case string:
		redacted, found := Text(typed)
		counts.Add(found)
		return redacted
	case []any:
		for i := range typed {
			typed[i] = walk(typed[i], counts)
		}
	case map[string]any:
		for key, item := range typed {
			typed[key] = walk(item, counts)
		}
	}
	return value
}
