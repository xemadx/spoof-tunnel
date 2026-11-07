package updater

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Release signatures.
//
// SHA256SUMS alone cannot prove a release is genuine: it is published in
// the same GitHub release as the binaries, so anyone able to publish a
// malicious release can publish matching checksums. Releases therefore
// also carry SHA256SUMS.sig, an Ed25519 signature of the exact SHA256SUMS
// bytes. The private signing key never lives in this repository; it is
// maintained by the release authority (either air-gapped using cmd/sign-release
// or configured as a protected environment secret SPOOF_RELEASE_SIGNING_KEY
// for automated release workflows). The public half is embedded below.
//
// Enforcement starts with minSignedVersion. Older releases predate signing
// and are still accepted (their signature, if present, is verified); from
// minSignedVersion on, a missing or invalid signature rejects the update.
const minSignedVersion = "v0.4.4"

const releaseSigningPublicKeyHex = "17bf9997b7306e394b3ae02b92700c8e80672f5137480e2c023a59a24fbfe3d6"

// requiresSignature reports whether a release signature is mandatory for
// the given target tag.
func requiresSignature(target string) bool {
	parts := strings.Split(strings.TrimPrefix(target, "v"), ".")
	if len(parts) != 3 {
		return false
	}
	min := strings.Split(strings.TrimPrefix(minSignedVersion, "v"), ".")
	for i := 0; i < 3; i++ {
		got, err1 := strconv.Atoi(parts[i])
		want, err2 := strconv.Atoi(min[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if got != want {
			return got > want
		}
	}
	return true
}

func releaseSigningPublicKey() (ed25519.PublicKey, error) {
	return parseReleasePublicKey(releaseSigningPublicKeyHex)
}

func parseReleasePublicKey(hexKey string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(hexKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid_release_signing_key")
	}
	return ed25519.PublicKey(raw), nil
}

// verifyReleaseSignature checks the hex-encoded Ed25519 signature of the
// exact SHA256SUMS bytes against the embedded release key. sig may carry
// trailing whitespace/newlines.
func verifyReleaseSignature(sums, sig []byte) error {
	return verifyReleaseSignatureWithKey(sums, sig, releaseSigningPublicKeyHex)
}

func verifyReleaseSignatureWithKey(sums, sig []byte, hexKey string) error {
	key, err := parseReleasePublicKey(hexKey)
	if err != nil {
		return err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("invalid_release_signature")
	}
	if !ed25519.Verify(key, sums, raw) {
		return fmt.Errorf("release_signature_failed")
	}
	return nil
}
