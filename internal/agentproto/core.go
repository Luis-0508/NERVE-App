package agentproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Core authenticates every response on the agent API with its own Ed25519
// key. The agent pins that key at enrollment (its fingerprint is part of the
// enrollment token), so a network attacker can neither forge commands nor
// fake answers such as "credential revoked", and cannot reuse a genuine
// response for another request.
const (
	HeaderCoreKeyID     = "X-NERVE-Core-Key"
	HeaderCorePublicKey = "X-NERVE-Core-Public-Key"
	HeaderCoreSignature = "X-NERVE-Core-Signature"

	coreSignatureVersion = "NERVE-CORE-SIG-V1"
)

var ErrUnverifiedResponse = errors.New("the response is not signed by the pinned NERVE Core key")

// CoreFingerprint is the base64url SHA-256 of Core's public key, as embedded in
// enrollment tokens.
func CoreFingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// CoreKeyID is a short identifier for Core's key, sent with each response.
func CoreKeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:8])
}

// CoreResponseCanonical binds a response to the exact request it answers: the
// request's own signature covers credential, method, route, time, nonce and
// body, so including it ties the response to that single request.
func CoreResponseCanonical(keyID, requestSignature string, status int, body []byte) []byte {
	digest := sha256.Sum256(body)
	var buffer bytes.Buffer
	buffer.WriteString(coreSignatureVersion)
	buffer.WriteByte('\n')
	buffer.WriteString(keyID)
	buffer.WriteByte('\n')
	buffer.WriteString(requestSignature)
	buffer.WriteByte('\n')
	buffer.WriteString(strconv.Itoa(status))
	buffer.WriteByte('\n')
	buffer.WriteString(hex.EncodeToString(digest[:]))
	return buffer.Bytes()
}

// SignCoreResponse sets the response authentication headers.
func SignCoreResponse(header http.Header, key ed25519.PrivateKey, requestSignature string, status int, body []byte) {
	public := key.Public().(ed25519.PublicKey)
	keyID := CoreKeyID(public)
	header.Set(HeaderCoreKeyID, keyID)
	header.Set(HeaderCorePublicKey, base64.StdEncoding.EncodeToString(public))
	header.Set(HeaderCoreSignature, base64.StdEncoding.EncodeToString(ed25519.Sign(key, CoreResponseCanonical(keyID, requestSignature, status, body))))
}

// VerifyCoreResponse checks a response against the pinned Core key.
func VerifyCoreResponse(pinned ed25519.PublicKey, header http.Header, requestSignature string, status int, body []byte) error {
	if len(pinned) != ed25519.PublicKeySize {
		return ErrUnverifiedResponse
	}
	keyID := CoreKeyID(pinned)
	if header.Get(HeaderCoreKeyID) != keyID {
		return ErrUnverifiedResponse
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header.Get(HeaderCoreSignature)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ErrUnverifiedResponse
	}
	if !ed25519.Verify(pinned, CoreResponseCanonical(keyID, requestSignature, status, body), signature) {
		return ErrUnverifiedResponse
	}
	return nil
}

// CoreKeyFromResponse extracts Core's public key from a response and accepts
// it only if it matches the fingerprint from the enrollment token.
func CoreKeyFromResponse(header http.Header, fingerprint string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header.Get(HeaderCorePublicKey)))
	if err != nil || len(raw) != ed25519.PublicKeySize || CoreFingerprint(raw) != fingerprint {
		return nil, ErrUnverifiedResponse
	}
	return ed25519.PublicKey(raw), nil
}

// ParseCoreSigningKey decodes NERVE_CORE_SIGNING_KEY: a 32-byte Ed25519 seed as
// 64 hex characters (the format setup.bat generates) or standard base64.
func ParseCoreSigningKey(value string) (ed25519.PrivateKey, error) {
	value = strings.TrimSpace(value)
	seed, err := hex.DecodeString(value)
	if err != nil {
		seed, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("NERVE_CORE_SIGNING_KEY must be a 32-byte seed as 64 hex characters or base64")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
