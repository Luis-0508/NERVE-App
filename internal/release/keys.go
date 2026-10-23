package release

import "crypto/ed25519"

// pinnedKeys are the release public keys this build trusts, base64 encoded.
// Add the public key printed by `nerve-release keygen` when the maintainer
// creates the release key; keep an old key next to a new one while agents
// built with only the old key are still in use. With no key pinned, release
// verification fails closed.
var pinnedKeys = []string{
	// Key ID wDVWMqmaZhZQWUI20kdQUfJM3aXgESh3fA44EWaVGDk, created 2026-10-23.
	"ZfrKtNPIHzdieje+sYRzmlauUlOG2d650s1/qTKCHz4=",
}

// PinnedKeys returns the release keys compiled into this build.
func PinnedKeys() []ed25519.PublicKey {
	keys := make([]ed25519.PublicKey, 0, len(pinnedKeys))
	for _, value := range pinnedKeys {
		key, err := ParsePublicKey(value)
		if err != nil {
			panic("release: invalid pinned key: " + err.Error())
		}
		keys = append(keys, key)
	}
	return keys
}
