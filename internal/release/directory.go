package release

import (
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// VerifyDirectory verifies manifest.json and its signature in dir, then every
// listed file. Files in dir that the manifest does not list are ignored.
func VerifyDirectory(pinned []ed25519.PublicKey, dir string) (Manifest, error) {
	manifest, err := readLimited(filepath.Join(dir, ManifestName), maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	signature, err := readLimited(filepath.Join(dir, SignatureName), 4096)
	if err != nil {
		return Manifest{}, err
	}
	parsed, err := VerifyManifest(pinned, manifest, signature)
	if err != nil {
		return Manifest{}, err
	}
	for _, file := range parsed.Files {
		// Validate guarantees a plain file name, so the path stays inside dir.
		content, err := os.Open(filepath.Join(dir, file.Name))
		if err != nil {
			return Manifest{}, fmt.Errorf("open %s: %w", file.Name, err)
		}
		err = VerifyArtifact(parsed, file.Name, content)
		_ = content.Close()
		if err != nil {
			return Manifest{}, err
		}
	}
	return parsed, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%w: %s is too large", ErrInvalidManifest, filepath.Base(path))
	}
	return content, nil
}
