package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// PublicKey verifies release signatures (SPEC v0.4 §5, "Release signature").
// It is a variable only so tests can use a key of their own.
var PublicKey = mustPublicKey("H/tsEqhxPOKjmI14VJl9r7sUOG3mWQXHsQKY/Xp0ZEk=")

func mustPublicKey(b64 string) ed25519.PublicKey {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ed25519.PublicKeySize {
		panic("update: invalid embedded public key")
	}
	return ed25519.PublicKey(k)
}

// SignedMessage is the exact message signed for a release (used by
// tools/sign and by the verifier): it binds the version to the exe hash,
// so an old signed exe cannot be served under a newer tag.
func SignedMessage(version string, sum [32]byte) []byte {
	return []byte("jiraffe-release-v1\n" + version + "\n" + hex.EncodeToString(sum[:]) + "\n")
}

// ParseSignature reads the .sig asset: one line, base64 of 64 bytes.
func ParseSignature(b []byte) ([]byte, error) {
	if len(b) > maxSig {
		return nil, &Error{Code: CodeBadSignature, Detail: "signature file too large"}
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, &Error{Code: CodeBadSignature, Detail: "malformed signature"}
	}
	return sig, nil
}

// VerifySignature checks sig over SignedMessage(version, sum) with key.
func VerifySignature(key ed25519.PublicKey, version string, sum [32]byte, sig []byte) bool {
	return len(key) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize &&
		ed25519.Verify(key, SignedMessage(version, sum), sig)
}
