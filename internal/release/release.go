// Package release describes signed NERVE releases and verifies them offline.
//
// A release is a flat set of files plus manifest.json, which lists every file
// with its SHA-256 and size, and manifest.json.sig, an Ed25519 signature over
// the exact manifest bytes. Verification needs nothing but the pinned release
// public keys compiled into the binary (keys.go), so an agent can check an
// update without trusting the server or the network it came from.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const (
	ManifestName  = "manifest.json"
	SignatureName = "manifest.json.sig"
	schemaVersion = 1
	// maxManifestBytes bounds what a verifier reads before checking the signature.
	maxManifestBytes = 1 << 20
)

// Kinds of files in a release.
const (
	KindAgent     = "agent"
	KindInstaller = "installer"
	KindSBOM      = "sbom"
)

var (
	ErrNoPinnedKeys     = errors.New("no release signing key is pinned in this build")
	ErrUnknownKey       = errors.New("the release is signed by a key this build does not trust")
	ErrBadSignature     = errors.New("the release signature is invalid")
	ErrInvalidManifest  = errors.New("the release manifest is invalid")
	ErrFileNotListed    = errors.New("the file is not part of the signed release")
	ErrDigestMismatch   = errors.New("the file does not match the signed release")
	ErrNoPlatformBuild  = errors.New("the release has no agent build for this platform")
	versionPattern      = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	fileNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platformPattern     = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	errDuplicateFile    = errors.New("duplicate file")
	errReservedFileName = errors.New("reserved file name")
)

// Manifest is the signed description of one release.
type Manifest struct {
	SchemaVersion int `json:"schemaVersion"`
	// Version is the release tag, for example v0.1.0.
	Version string `json:"version"`
	// MinimumCoreVersion is the oldest Core release this agent supports; empty
	// when there is no constraint.
	MinimumCoreVersion string    `json:"minimumCoreVersion,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	Files              []File    `json:"files"`
}

// File is one release file.
type File struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	OS     string `json:"os,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Signature is the content of manifest.json.sig.
type Signature struct {
	KeyID     string `json:"keyId"`
	Signature string `json:"signature"`
}

// KeyID identifies a release public key: base64url SHA-256 of the key.
func KeyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ParsePublicKey decodes a base64 (standard or URL) Ed25519 public key.
func ParsePublicKey(value string) (ed25519.PublicKey, error) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := encoding.DecodeString(value); err == nil && len(raw) == ed25519.PublicKeySize {
			return ed25519.PublicKey(raw), nil
		}
	}
	return nil, errors.New("a release public key is 32 bytes, base64 encoded")
}

// ParsePrivateKey decodes a 32-byte Ed25519 seed given as 64 hex characters or base64.
func ParsePrivateKey(value string) (ed25519.PrivateKey, error) {
	value = strings.TrimSpace(value)
	if raw, err := hex.DecodeString(value); err == nil && len(raw) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(raw), nil
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := encoding.DecodeString(value); err == nil && len(raw) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(raw), nil
		}
	}
	return nil, errors.New("a release signing key is a 32-byte seed as 64 hex characters or base64")
}

// Sign returns the detached signature for manifest bytes.
func Sign(key ed25519.PrivateKey, manifest []byte) ([]byte, error) {
	signature := Signature{KeyID: KeyID(key.Public().(ed25519.PublicKey)),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, manifest))}
	encoded, err := json.MarshalIndent(signature, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// VerifyManifest checks the signature over the exact manifest bytes with one of
// the pinned keys, then parses and validates the manifest. Nothing in the
// manifest is trusted before the signature is verified.
func VerifyManifest(pinned []ed25519.PublicKey, manifest, signature []byte) (Manifest, error) {
	if len(pinned) == 0 {
		return Manifest{}, ErrNoPinnedKeys
	}
	if len(manifest) > maxManifestBytes || len(signature) > 4096 {
		return Manifest{}, ErrInvalidManifest
	}
	var detached Signature
	if err := json.Unmarshal(signature, &detached); err != nil {
		return Manifest{}, ErrBadSignature
	}
	var key ed25519.PublicKey
	for _, candidate := range pinned {
		if KeyID(candidate) == detached.KeyID {
			key = candidate
			break
		}
	}
	if key == nil {
		return Manifest{}, ErrUnknownKey
	}
	raw, err := base64.StdEncoding.DecodeString(detached.Signature)
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(key, manifest, raw) {
		return Manifest{}, ErrBadSignature
	}
	var parsed Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := parsed.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	return parsed, nil
}

// Validate checks the manifest structure. File names are plain names without
// directories, so a verifier never writes or reads outside its release directory.
func (m Manifest) Validate() error {
	if m.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported schema version %d", m.SchemaVersion)
	}
	if !versionPattern.MatchString(m.Version) {
		return fmt.Errorf("version %q is not a vMAJOR.MINOR.PATCH tag", m.Version)
	}
	if m.MinimumCoreVersion != "" && !versionPattern.MatchString(m.MinimumCoreVersion) {
		return fmt.Errorf("minimum Core version %q is not a vMAJOR.MINOR.PATCH tag", m.MinimumCoreVersion)
	}
	if len(m.Files) == 0 || len(m.Files) > 256 {
		return errors.New("a release lists 1 to 256 files")
	}
	seen := map[string]bool{}
	for _, file := range m.Files {
		switch {
		case !fileNamePattern.MatchString(file.Name):
			return fmt.Errorf("file name %q is not a plain file name", file.Name)
		case file.Name == ManifestName || file.Name == SignatureName:
			return fmt.Errorf("%w: %s", errReservedFileName, file.Name)
		case seen[file.Name]:
			return fmt.Errorf("%w: %s", errDuplicateFile, file.Name)
		case file.Kind != KindAgent && file.Kind != KindInstaller && file.Kind != KindSBOM:
			return fmt.Errorf("file %s has unknown kind %q", file.Name, file.Kind)
		case file.Kind == KindAgent && (!platformPattern.MatchString(file.OS) || !platformPattern.MatchString(file.Arch)):
			return fmt.Errorf("agent file %s needs os and arch", file.Name)
		case file.Size < 0 || !digestPattern.MatchString(file.SHA256):
			return fmt.Errorf("file %s has an invalid size or digest", file.Name)
		}
		seen[file.Name] = true
	}
	return nil
}

// Lookup returns the listed file with this name.
func (m Manifest) Lookup(name string) (File, bool) {
	for _, file := range m.Files {
		if file.Name == name {
			return file, true
		}
	}
	return File{}, false
}

// AgentFor returns the agent build for a platform (GOOS/GOARCH).
func (m Manifest) AgentFor(goos, goarch string) (File, error) {
	for _, file := range m.Files {
		if file.Kind == KindAgent && file.OS == goos && file.Arch == goarch {
			return file, nil
		}
	}
	return File{}, fmt.Errorf("%w (%s/%s)", ErrNoPlatformBuild, goos, goarch)
}

// VerifyArtifact checks that content matches the signed entry for name. It
// reads at most the signed size plus one byte.
func VerifyArtifact(m Manifest, name string, content io.Reader) error {
	file, ok := m.Lookup(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrFileNotListed, name)
	}
	digest := sha256.New()
	read, err := io.Copy(digest, io.LimitReader(content, file.Size+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if read != file.Size || hex.EncodeToString(digest.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("%w: %s", ErrDigestMismatch, name)
	}
	return nil
}

// DescribeFile returns the manifest entry for content.
func DescribeFile(name, kind, goos, goarch string, content io.Reader) (File, error) {
	digest := sha256.New()
	size, err := io.Copy(digest, content)
	if err != nil {
		return File{}, err
	}
	return File{Name: name, Kind: kind, OS: goos, Arch: goarch, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
