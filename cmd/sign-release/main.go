// Command sign-release signs the release SHA256SUMS file with an offline
// Ed25519 key and verifies such signatures.
//
// The private key never lives in this repository. It is held by the release
// operator as the GitHub Actions secret SPOOF_RELEASE_SIGNING_KEY (hex of
// the 32-byte seed). The matching public key is embedded in the panel
// updater (panel/backend/internal/updater/sign.go) so the updater can
// verify a release without trusting the GitHub account alone.
//
// Usage:
//
//	sign-release --gen-key
//	  Prints a fresh private seed and public key (hex). The operator stores
//	  the seed as a secret and sends the public key to be embedded.
//
//	sign-release --key <hex-seed> --file SHA256SUMS --out SHA256SUMS.sig
//	  Writes the hex-encoded Ed25519 signature of the file.
//
//	sign-release --verify --pub <hex-pubkey> --file SHA256SUMS --sig SHA256SUMS.sig
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
)

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sign-release:", err)
	os.Exit(1)
}

func decodeHex(name, value string, size int) []byte {
	b, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(b) != size {
		fatal(fmt.Errorf("%s must be %d hex-encoded bytes", name, size))
	}
	return b
}

func main() {
	genKey := flag.Bool("gen-key", false, "generate a fresh keypair and print it")
	verify := flag.Bool("verify", false, "verify a signature instead of signing")
	keyHex := flag.String("key", "", "hex-encoded 32-byte private seed")
	pubHex := flag.String("pub", "", "hex-encoded 32-byte public key (for --verify)")
	file := flag.String("file", "", "file to sign or verify")
	sigFile := flag.String("sig", "", "signature file to read (for --verify)")
	out := flag.String("out", "", "output signature file (for signing)")
	flag.Parse()

	if *genKey {
		var seed [32]byte
		if _, err := rand.Read(seed[:]); err != nil {
			fatal(err)
		}
		priv := ed25519.NewKeyFromSeed(seed[:])
		fmt.Printf("SPOOF_SIGNING_KEY=%s\n", hex.EncodeToString(seed[:]))
		fmt.Printf("PUBLIC KEY (embed in updater/sign.go)=%s\n", hex.EncodeToString(priv.Public().(ed25519.PublicKey)))
		return
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		fatal(err)
	}

	if *verify {
		pub := decodeHex("public key", *pubHex, ed25519.PublicKeySize)
		raw, err := os.ReadFile(*sigFile)
		if err != nil {
			fatal(err)
		}
		sig := decodeHex("signature", string(raw), ed25519.SignatureSize)
		if !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
			fatal(fmt.Errorf("signature verification failed"))
		}
		fmt.Println("signature OK")
		return
	}

	seed := decodeHex("private seed", *keyHex, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seed)
	sig := ed25519.Sign(priv, data)
	if *out == "" {
		fatal(fmt.Errorf("--out is required when signing"))
	}
	if err := os.WriteFile(*out, []byte(hex.EncodeToString(sig)+"\n"), 0600); err != nil {
		fatal(err)
	}
	fmt.Printf("signed %s -> %s\n", *file, *out)
}
