// Package agentproto defines the wire protocol between Core and the NERVE
// management agent. It has no database or HTTP-server dependencies so the agent
// binary and the API share one definition of request signing.
package agentproto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Request authentication headers. Every agent request is signed with the
// agent's Ed25519 key; no reusable secret is ever sent over the network.
const (
	HeaderCredential = "X-NERVE-Credential"
	HeaderTimestamp  = "X-NERVE-Timestamp"
	HeaderNonce      = "X-NERVE-Nonce"
	HeaderSignature  = "X-NERVE-Signature"
	HeaderServerTime = "X-NERVE-Server-Time"

	// CredentialSelfSigned marks the enrollment request, which is signed with the
	// key it registers (proof of possession) rather than an enrolled credential.
	CredentialSelfSigned = "enroll"

	signatureVersion = "NERVE-AGENT-SIG-V1"
	// MaxClockSkew bounds how far an agent clock may drift from Core. Nonces are
	// retained for twice this window, so a captured request cannot be replayed.
	MaxClockSkew = 2 * time.Minute
	// APIPrefix is stripped from request paths before signing so a reverse proxy
	// that mounts the API elsewhere does not invalidate signatures.
	APIPrefix = "/api/v1"
)

var (
	ErrMissingSignature = errors.New("agent request signature is missing")
	ErrMalformed        = errors.New("agent request signature is malformed")
	ErrClockSkew        = errors.New("agent request timestamp is outside the accepted clock skew")
	ErrBadSignature     = errors.New("agent request signature is invalid")
)

// SignedHeaders carries the parsed authentication headers of one request.
type SignedHeaders struct {
	CredentialID string
	Timestamp    int64
	Nonce        []byte
	Signature    []byte
}

// Canonical builds the exact byte string that is signed. It binds the
// credential, method, route (path relative to /api/v1, plus raw query),
// timestamp, nonce and a digest of the body.
func Canonical(credentialID, method, route string, timestamp int64, nonce []byte, body []byte) []byte {
	digest := sha256.Sum256(body)
	var buffer bytes.Buffer
	buffer.WriteString(signatureVersion)
	buffer.WriteByte('\n')
	buffer.WriteString(credentialID)
	buffer.WriteByte('\n')
	buffer.WriteString(strings.ToUpper(method))
	buffer.WriteByte('\n')
	buffer.WriteString(route)
	buffer.WriteByte('\n')
	buffer.WriteString(strconv.FormatInt(timestamp, 10))
	buffer.WriteByte('\n')
	buffer.WriteString(hex.EncodeToString(nonce))
	buffer.WriteByte('\n')
	buffer.WriteString(hex.EncodeToString(digest[:]))
	return buffer.Bytes()
}

// Route returns the signed route for a request URL path and raw query.
func Route(path, rawQuery string) string {
	route := strings.TrimPrefix(path, APIPrefix)
	if rawQuery != "" {
		route += "?" + rawQuery
	}
	return route
}

// Sign adds authentication headers to request. body must be the exact request body.
func Sign(request *http.Request, credentialID string, key ed25519.PrivateKey, body []byte, now time.Time) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("agent signing key is invalid")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate request nonce: %w", err)
	}
	timestamp := now.Unix()
	canonical := Canonical(credentialID, request.Method, Route(request.URL.Path, request.URL.RawQuery), timestamp, nonce, body)
	request.Header.Set(HeaderCredential, credentialID)
	request.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp, 10))
	request.Header.Set(HeaderNonce, hex.EncodeToString(nonce))
	request.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(ed25519.Sign(key, canonical)))
	return nil
}

// ParseHeaders extracts and syntactically validates authentication headers.
func ParseHeaders(header http.Header) (SignedHeaders, error) {
	credential := strings.TrimSpace(header.Get(HeaderCredential))
	rawTimestamp := header.Get(HeaderTimestamp)
	rawNonce := header.Get(HeaderNonce)
	rawSignature := header.Get(HeaderSignature)
	if credential == "" && rawTimestamp == "" && rawNonce == "" && rawSignature == "" {
		return SignedHeaders{}, ErrMissingSignature
	}
	if credential == "" || len(credential) > 64 {
		return SignedHeaders{}, ErrMalformed
	}
	timestamp, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return SignedHeaders{}, ErrMalformed
	}
	nonce, err := hex.DecodeString(rawNonce)
	if err != nil || len(nonce) != 16 {
		return SignedHeaders{}, ErrMalformed
	}
	signature, err := base64.StdEncoding.DecodeString(rawSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return SignedHeaders{}, ErrMalformed
	}
	return SignedHeaders{CredentialID: credential, Timestamp: timestamp, Nonce: nonce, Signature: signature}, nil
}

// CheckTimestamp rejects requests outside the accepted clock-skew window.
func (h SignedHeaders) CheckTimestamp(now time.Time) error {
	if now.Sub(time.Unix(h.Timestamp, 0)).Abs() > MaxClockSkew {
		return ErrClockSkew
	}
	return nil
}

// Verify checks the signature of a request against publicKey.
func (h SignedHeaders) Verify(publicKey ed25519.PublicKey, method, route string, body []byte) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	if !ed25519.Verify(publicKey, Canonical(h.CredentialID, method, route, h.Timestamp, h.Nonce, body), h.Signature) {
		return ErrBadSignature
	}
	return nil
}

// Fingerprint is the hex SHA-256 of a public key. Operators compare it with the
// value printed by the agent at enrollment.
func Fingerprint(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return hex.EncodeToString(sum[:])
}

// EncodePublicKey and DecodePublicKey use standard base64 for JSON transport.
func EncodePublicKey(publicKey ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(publicKey)
}

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("public key must be a base64-encoded 32-byte Ed25519 key")
	}
	return ed25519.PublicKey(raw), nil
}
