package redact

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func TestTextRedactsSecrets(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
		counts            Counts
	}{
		{"password assignment", "connecting with password=hunter2 now", "connecting with password=[redacted:password] now", Counts{KindPassword: 1}},
		{"colon and quotes", `pwd: "s3cr3t value"` + "\n" + `api_key='abc123'`, "pwd: [redacted:password]\napi_key=[redacted:password]", Counts{KindPassword: 2}},
		{"connection string", "Server=db;User Id=sa;Password=P@ss;Encrypt=true", "Server=db;User Id=sa;Password=[redacted:password];Encrypt=true", Counts{KindPassword: 1}},
		{"url credentials", "fetching https://admin:topsecret@example.com/repo.git", "fetching https://admin:[redacted:url_credentials]@example.com/repo.git", Counts{KindURLCredentials: 1}},
		{"bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.abc_DEF-123", "Authorization: Bearer [redacted:bearer_token]", Counts{KindBearerToken: 1}},
		{"aws access key", "key AKIAIOSFODNN7EXAMPLE in use", "key [redacted:aws_access_key] in use", Counts{KindAWSAccessKey: 1}},
		{"aws secret", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "aws_secret_access_key = [redacted:password]", Counts{KindPassword: 1}},
		{"private key block", "before\n-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----\nafter",
			"before\n[redacted:private_key]\nafter", Counts{KindPrivateKey: 1}},
		{"truncated private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIEow", "[redacted:private_key]", Counts{KindPrivateKey: 1}},
		{"several kinds", "token=abc https://u:p@h Bearer abcdefghij", "token=[redacted:password] https://u:[redacted:url_credentials]@h Bearer [redacted:bearer_token]",
			Counts{KindPassword: 1, KindURLCredentials: 1, KindBearerToken: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, counts := Text(test.input)
			if got != test.want || !maps.Equal(counts, test.counts) {
				t.Fatalf("Text(%q) = %q, %v; want %q, %v", test.input, got, counts, test.want, test.counts)
			}
		})
	}
}

func TestTextLeavesOrdinaryOutput(t *testing.T) {
	for _, input := range []string{
		"Password policy: minimum length 12",
		"The password was changed successfully.",
		"bearer of bad news",
		"Bearer short",
		"https://example.com:8443/path and user@example.com",
		"tokens remaining 5",
		"-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----",
		"akiaiosfodnn7example",
		"password=",
		"",
	} {
		if got, counts := Text(input); got != input || len(counts) != 0 {
			t.Errorf("Text(%q) = %q, %v; want it unchanged", input, got, counts)
		}
	}
}

func TestRedactionIsIdempotent(t *testing.T) {
	input := "password=a token: 'b' https://u:c@h Bearer abcdefghijk AKIAIOSFODNN7EXAMPLE\n-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"
	once, _ := Text(input)
	twice, counts := Text(once)
	if twice != once || len(counts) != 0 {
		t.Fatalf("second pass changed %q to %q or counted %v", once, twice, counts)
	}
	if strings.Contains(once, "abcdefghijk") || strings.Contains(once, "AKIA") {
		t.Fatalf("secret survived: %q", once)
	}
}

func TestJSONRedactsStringValues(t *testing.T) {
	input := json.RawMessage(`{"processes":[{"name":"psql","commandLine":"psql postgres://app:pw@db/app","pid":4242,"cpu":1.5}],"password":"key-names-stay"}`)
	got, counts := JSON(input)
	var decoded struct {
		Processes []struct {
			CommandLine string      `json:"commandLine"`
			PID         json.Number `json:"pid"`
			CPU         json.Number `json:"cpu"`
		} `json:"processes"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("redacted JSON does not parse: %v (%s)", err, got)
	}
	if decoded.Processes[0].CommandLine != "psql postgres://app:[redacted:url_credentials]@db/app" || decoded.Processes[0].PID != "4242" ||
		decoded.Processes[0].CPU != "1.5" || decoded.Password != "key-names-stay" || !maps.Equal(counts, Counts{KindURLCredentials: 1}) {
		t.Fatalf("JSON(%s) = %s, %v", input, got, counts)
	}
	if again, counts := JSON(got); string(again) != string(got) || len(counts) != 0 {
		t.Fatalf("second JSON pass changed %s to %s (%v)", got, again, counts)
	}
	clean := json.RawMessage(`{ "name" : "unchanged formatting" }`)
	if same, counts := JSON(clean); string(same) != string(clean) || len(counts) != 0 {
		t.Fatalf("JSON without secrets was rewritten: %s", same)
	}
}
