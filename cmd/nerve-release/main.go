// Command nerve-release creates and checks signed NERVE releases. The release
// workflow uses it to write and sign manifest.json; maintainers use it once to
// create the release key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nerve-labs/nerve/internal/release"
)

const usage = `Usage:
  nerve-release keygen -out FILE              create a release signing key (private seed in FILE)
  nerve-release manifest -dir DIR -version V  write DIR/manifest.json for the files in DIR
  nerve-release sign -dir DIR                 sign DIR/manifest.json with NERVE_RELEASE_SIGNING_KEY
  nerve-release verify -dir DIR [-key B64]    verify DIR against -key (repeatable) or the pinned keys
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "manifest":
		err = writeManifest(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nerve-release:", err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := flags.String("out", "", "file for the private key seed (must not exist)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, hex.EncodeToString(private.Seed())); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("Private key seed written to %s. Store it as the GitHub secret NERVE_RELEASE_SIGNING_KEY, then delete the file.\n", *out)
	fmt.Printf("Public key (add to internal/release/keys.go): %s\nKey ID: %s\n", base64.StdEncoding.EncodeToString(public), release.KeyID(public))
	return nil
}

// writeManifest lists every file in dir. Agent binaries are named
// nerve-agent_<os>_<arch>[.exe]; *.ps1 and *.sh are installers; *.spdx.json are SBOMs.
func writeManifest(args []string) error {
	flags := flag.NewFlagSet("manifest", flag.ContinueOnError)
	dir := flags.String("dir", "", "release directory")
	version := flags.String("version", "", "release tag, for example v0.1.0")
	minimumCore := flags.String("min-core", "", "oldest supported Core release (optional)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	entries, err := os.ReadDir(*dir)
	if err != nil {
		return err
	}
	manifest := release.Manifest{SchemaVersion: 1, Version: *version, MinimumCoreVersion: *minimumCore, CreatedAt: createdAt()}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == release.ManifestName || name == release.SignatureName {
			continue
		}
		kind, goos, goarch, err := classify(name)
		if err != nil {
			return err
		}
		content, err := os.Open(filepath.Join(*dir, name))
		if err != nil {
			return err
		}
		file, err := release.DescribeFile(name, kind, goos, goarch, content)
		_ = content.Close()
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, file)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Name < manifest.Files[j].Name })
	if err := manifest.Validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, release.ManifestName), append(encoded, '\n'), 0o644)
}

func classify(name string) (kind, goos, goarch string, err error) {
	switch {
	case strings.HasPrefix(name, "nerve-agent_"):
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "nerve-agent_"), ".exe"), "_")
		if len(parts) != 2 {
			return "", "", "", fmt.Errorf("agent file %s is not named nerve-agent_<os>_<arch>", name)
		}
		return release.KindAgent, parts[0], parts[1], nil
	case strings.HasSuffix(name, ".ps1"), strings.HasSuffix(name, ".sh"):
		return release.KindInstaller, "", "", nil
	case strings.HasSuffix(name, ".spdx.json"):
		return release.KindSBOM, "", "", nil
	}
	return "", "", "", fmt.Errorf("unexpected release file %s", name)
}

// createdAt follows SOURCE_DATE_EPOCH, so a rebuild of the same tag yields the same manifest.
func createdAt() time.Time {
	if value := os.Getenv("SOURCE_DATE_EPOCH"); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	return time.Now().UTC().Truncate(time.Second)
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	dir := flags.String("dir", "", "release directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// The key comes only from the environment: never a file in the workspace or an argument.
	key, err := release.ParsePrivateKey(os.Getenv("NERVE_RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile(filepath.Join(*dir, release.ManifestName))
	if err != nil {
		return err
	}
	signature, err := release.Sign(key, manifest)
	if err != nil {
		return err
	}
	// Never sign something an agent would reject.
	if _, err := release.VerifyManifest([]ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, manifest, signature); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*dir, release.SignatureName), signature, 0o644); err != nil {
		return err
	}
	fmt.Println("Signed with key", release.KeyID(key.Public().(ed25519.PublicKey)))
	return nil
}

type keyList []string

func (k *keyList) String() string     { return strings.Join(*k, ",") }
func (k *keyList) Set(v string) error { *k = append(*k, v); return nil }

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := flags.String("dir", "", "release directory")
	var keys keyList
	flags.Var(&keys, "key", "trusted public key, base64 (repeatable; default: the keys pinned in this build)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	pinned := release.PinnedKeys()
	if len(keys) > 0 {
		pinned = nil
		for _, value := range keys {
			key, err := release.ParsePublicKey(value)
			if err != nil {
				return err
			}
			pinned = append(pinned, key)
		}
	}
	manifest, err := release.VerifyDirectory(pinned, *dir)
	if err != nil {
		return err
	}
	fmt.Printf("Release %s verified: %d files match the signed manifest.\n", manifest.Version, len(manifest.Files))
	return nil
}
