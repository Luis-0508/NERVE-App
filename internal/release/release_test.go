package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRelease(t *testing.T) (ed25519.PrivateKey, []byte, []byte, map[string][]byte) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"nerve-agent_linux_amd64":       []byte("linux binary"),
		"nerve-agent_windows_amd64.exe": []byte("windows binary"),
		"install-linux.sh":              []byte("#!/bin/sh\n"),
		"nerve-agent.spdx.json":         []byte("{}"),
	}
	manifest := Manifest{SchemaVersion: 1, Version: "v0.1.0", CreatedAt: time.Unix(1_800_000_000, 0).UTC()}
	for name, content := range files {
		kind, goos, goarch := KindInstaller, "", ""
		switch {
		case strings.HasPrefix(name, "nerve-agent_linux"):
			kind, goos, goarch = KindAgent, "linux", "amd64"
		case strings.HasPrefix(name, "nerve-agent_windows"):
			kind, goos, goarch = KindAgent, "windows", "amd64"
		case strings.HasSuffix(name, ".spdx.json"):
			kind = KindSBOM
		}
		file, err := DescribeFile(name, kind, goos, goarch, bytes.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		manifest.Files = append(manifest.Files, file)
	}
	encoded, _ := json.Marshal(manifest)
	signature, err := Sign(key, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return key, encoded, signature, files
}

func TestVerifyManifest(t *testing.T) {
	key, manifest, signature, files := testRelease(t)
	pinned := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	parsed, err := VerifyManifest(pinned, manifest, signature)
	if err != nil || parsed.Version != "v0.1.0" || len(parsed.Files) != len(files) {
		t.Fatalf("valid release: %+v %v", parsed, err)
	}
	for name, content := range files {
		if err := VerifyArtifact(parsed, name, bytes.NewReader(content)); err != nil {
			t.Fatalf("valid artifact %s: %v", name, err)
		}
	}

	tampered := bytes.Replace(manifest, []byte(`"v0.1.0"`), []byte(`"v0.1.1"`), 1)
	if _, err := VerifyManifest(pinned, tampered, signature); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered manifest: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if _, err := VerifyManifest([]ed25519.PublicKey{other.Public().(ed25519.PublicKey)}, manifest, signature); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("unknown key: %v", err)
	}
	// A signature that claims a pinned key ID but was made with another key.
	forged, _ := Sign(other, manifest)
	forged = bytes.Replace(forged, []byte(KeyID(other.Public().(ed25519.PublicKey))), []byte(KeyID(pinned[0])), 1)
	if _, err := VerifyManifest(pinned, manifest, forged); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("forged signature: %v", err)
	}
	if _, err := VerifyManifest(nil, manifest, signature); !errors.Is(err, ErrNoPinnedKeys) {
		t.Fatalf("no pinned keys: %v", err)
	}

	if err := VerifyArtifact(parsed, "nerve-agent_linux_amd64", strings.NewReader("linux binarY")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("tampered artifact: %v", err)
	}
	if err := VerifyArtifact(parsed, "nerve-agent_linux_amd64", strings.NewReader("linux binary and more")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("extended artifact: %v", err)
	}
	if err := VerifyArtifact(parsed, "other.exe", strings.NewReader("x")); !errors.Is(err, ErrFileNotListed) {
		t.Fatalf("unlisted artifact: %v", err)
	}

	if file, err := parsed.AgentFor("windows", "amd64"); err != nil || file.Name != "nerve-agent_windows_amd64.exe" {
		t.Fatalf("windows build: %+v %v", file, err)
	}
	if _, err := parsed.AgentFor("linux", "arm64"); !errors.Is(err, ErrNoPlatformBuild) {
		t.Fatalf("wrong platform: %v", err)
	}
}

func TestManifestValidationRejectsUnsafeEntries(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	pinned := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	digest := strings.Repeat("a", 64)
	for name, manifest := range map[string]string{
		"path traversal":   `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"../evil","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"directory":        `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a/b","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"reserved name":    `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"manifest.json","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"bad version":      `{"schemaVersion":1,"version":"1.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a.sh","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"unknown kind":     `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a.sh","kind":"script","size":1,"sha256":"` + digest + `"}]}`,
		"agent no os":      `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a","kind":"agent","size":1,"sha256":"` + digest + `"}]}`,
		"duplicate":        `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a.sh","kind":"installer","size":1,"sha256":"` + digest + `"},{"name":"a.sh","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"unknown field":    `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","url":"http://x","files":[{"name":"a.sh","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"newer schema":     `{"schemaVersion":2,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a.sh","kind":"installer","size":1,"sha256":"` + digest + `"}]}`,
		"uppercase digest": `{"schemaVersion":1,"version":"v1.0.0","createdAt":"2026-10-22T00:00:00Z","files":[{"name":"a.sh","kind":"installer","size":1,"sha256":"` + strings.ToUpper(digest) + `"}]}`,
	} {
		signature, _ := Sign(key, []byte(manifest))
		if _, err := VerifyManifest(pinned, []byte(manifest), signature); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestVerifyDirectory(t *testing.T) {
	key, manifest, signature, files := testRelease(t)
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, ManifestName), manifest, 0o644)
	_ = os.WriteFile(filepath.Join(dir, SignatureName), signature, 0o644)
	pinned := []ed25519.PublicKey{key.Public().(ed25519.PublicKey)}
	if _, err := VerifyDirectory(pinned, dir); err != nil {
		t.Fatalf("valid directory: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "install-linux.sh"), []byte("#!/bin/sh\nrm -rf /\n"), 0o644)
	if _, err := VerifyDirectory(pinned, dir); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("replaced installer: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "install-linux.sh"))
	if _, err := VerifyDirectory(pinned, dir); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestKeyParsingAndPinnedKeys(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(nil)
	parsed, err := ParsePrivateKey(hex.EncodeToString(private.Seed()))
	if err != nil || !parsed.Equal(private) {
		t.Fatalf("hex seed: %v", err)
	}
	if _, err := ParsePrivateKey("short"); err == nil {
		t.Fatal("short seed accepted")
	}
	encoded, _ := json.Marshal(public)
	if key, err := ParsePublicKey(strings.Trim(string(encoded), `"`)); err != nil || !key.Equal(public) {
		t.Fatalf("public key: %v", err)
	}
	// Every compiled-in key must parse, or PinnedKeys would panic at runtime.
	_ = PinnedKeys()
}
