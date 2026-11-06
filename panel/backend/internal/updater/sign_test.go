package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequiresSignature(t *testing.T) {
	for target, want := range map[string]bool{
		"v0.4.3": false,
		"v0.4.4": true,
		"v0.4.5": true,
		"v0.5.0": true,
		"v1.0.0": true,
		"dev":    false,
		"v0.4":   false,
	} {
		if requiresSignature(target) != want {
			t.Fatalf("requiresSignature(%s) = %v, want %v", target, !want, want)
		}
	}
}

func TestVerifyReleaseSignatureWithKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyHex := hex.EncodeToString(pub)
	sums := []byte("deadbeef00000000000000000000000000000000000000000000000000000000  spoof-tunnel-linux-amd64.tar.gz\n")
	sig := ed25519.Sign(priv, sums)
	sigHex := hex.EncodeToString(sig) + "\n"
	if err := verifyReleaseSignatureWithKey(sums, []byte(sigHex), keyHex); err != nil {
		t.Fatal("valid signature rejected:", err)
	}
	if err := verifyReleaseSignatureWithKey(append(sums, ' '), []byte(sigHex), keyHex); err == nil {
		t.Fatal("tampered checksums accepted")
	}
	if err := verifyReleaseSignatureWithKey(sums, []byte(hex.EncodeToString(sig[:63])), keyHex); err == nil {
		t.Fatal("truncated signature accepted")
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSig := hex.EncodeToString(ed25519.Sign(other, sums))
	if err := verifyReleaseSignatureWithKey(sums, []byte(otherSig), keyHex); err == nil {
		t.Fatal("signature from another key accepted")
	}
	if err := verifyReleaseSignature(sums, []byte(sigHex)); err == nil {
		t.Fatal("test signature accepted against the embedded release key")
	}
}

func TestStartRejectsUnsignedNewRelease(t *testing.T) {
	tag := "v0.4.4"
	base := "https://github.com/xemadx/spoof-tunnel/releases/"
	release := Release{Tag: tag, URL: base + "tag/" + tag, Assets: []Asset{
		{Name: "spoof-tunnel-linux-amd64.tar.gz", URL: base + "download/" + tag + "/spoof-tunnel-linux-amd64.tar.gz", Size: 1024},
		{Name: "SHA256SUMS", URL: base + "download/" + tag + "/SHA256SUMS", Size: 196},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(release) }))
	defer server.Close()
	u := New("v0.4.1", t.TempDir())
	u.apiBase = server.URL
	u.allowed = func() (bool, string) { return true, "" }
	calls := 0
	u.command = func(ctx context.Context, args ...string) error { calls++; return nil }
	if _, err := u.Start(context.Background(), tag); err == nil || !strings.Contains(err.Error(), "missing_release_signature") {
		t.Fatalf("unsigned v0.4.4 accepted: err=%v calls=%d", err, calls)
	}
	if calls != 0 {
		t.Fatal("unsigned update started the service")
	}
}
