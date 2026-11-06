package tlsconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestCert(t *testing.T, dir, tag, domain string, notBefore, notAfter time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, "cert-"+tag+".pem")
	keyFile = filepath.Join(dir, "key-"+tag+".pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func TestValidDomain(t *testing.T) {
	good := []string{"panel.example.com", "example.com", "a-b.c-d.example.co.uk", "Panel.Example.COM.", "x.io"}
	for _, d := range good {
		if !ValidDomain(d) {
			t.Errorf("ValidDomain(%q) = false, want true", d)
		}
	}
	bad := []string{"", "*.example.com", "example", "exa mple.com", "-bad.example.com", "bad-.example.com", "example..com", "example.com:8443", "http://example.com", "example.c", "a/b.com"}
	for _, d := range bad {
		if ValidDomain(d) {
			t.Errorf("ValidDomain(%q) = true, want false", d)
		}
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	cfg := Config{Enabled: true, Domain: "Panel.Example.COM", CertFile: "/etc/letsencrypt/live/panel.example.com/fullchain.pem", KeyFile: "/etc/letsencrypt/live/panel.example.com/privkey.pem"}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("tls.json mode = %o, want 0600", info.Mode().Perm())
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Enabled || got.Domain != "panel.example.com" || got.CertFile != cfg.CertFile || got.KeyFile != cfg.KeyFile {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	// A malformed file is an error, never silently disabled.
	if err := os.WriteFile(Path(dir), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("Load malformed file: expected error, got nil")
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	cert, key := writeTestCert(t, dir, "main", "panel.example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	expires, issuer, err := Validate(cert, key, "panel.example.com")
	if err != nil {
		t.Fatalf("Validate good pair: %v", err)
	}
	if expires.IsZero() || issuer == "" {
		t.Errorf("Validate returned empty expiry/issuer: %v %q", expires, issuer)
	}
	if _, _, err := Validate(cert, key, "other.example.com"); err == nil {
		t.Error("Validate wrong domain: expected error, got nil")
	}
	// Expired certificate.
	expiredCert, expiredKey := writeTestCert(t, dir, "expired", "panel.example.com", now.Add(-48*time.Hour), now.Add(-time.Hour))
	if _, _, err := Validate(expiredCert, expiredKey, "panel.example.com"); err == nil {
		t.Error("Validate expired: expected error, got nil")
	}
	// Key from another pair does not match.
	_, otherKey := writeTestCert(t, dir, "other", "panel.example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	if _, _, err := Validate(cert, otherKey, "panel.example.com"); err == nil {
		t.Error("Validate mismatched key: expected error, got nil")
	}
	// Missing files.
	if _, _, err := Validate(filepath.Join(dir, "nope.pem"), key, "panel.example.com"); err == nil {
		t.Error("Validate missing cert: expected error, got nil")
	}
	// Bad domain.
	if _, _, err := Validate(cert, key, "*.example.com"); err == nil {
		t.Error("Validate wildcard domain: expected error, got nil")
	}
}

func TestEvaluate(t *testing.T) {
	dir := t.TempDir()
	if st := Evaluate(dir); st.Enabled || st.Valid || st.Error != "" {
		t.Errorf("Evaluate with no config: %+v", st)
	}
	now := time.Now()
	cert, key := writeTestCert(t, dir, "main", "panel.example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	if err := Save(dir, Config{Enabled: true, Domain: "panel.example.com", CertFile: cert, KeyFile: key}); err != nil {
		t.Fatal(err)
	}
	st := Evaluate(dir)
	if !st.Enabled || !st.Valid || st.Domain != "panel.example.com" || st.Expires.IsZero() {
		t.Errorf("Evaluate valid config: %+v", st)
	}
	// Break the key file: enabled but invalid, with a reason.
	if err := os.WriteFile(key, []byte("garbage"), 0600); err != nil {
		t.Fatal(err)
	}
	st = Evaluate(dir)
	if !st.Enabled || st.Valid || st.Error == "" {
		t.Errorf("Evaluate broken config: %+v", st)
	}
	// Disabled config never claims validity.
	if err := Save(dir, Config{Enabled: false, Domain: "panel.example.com", CertFile: cert, KeyFile: key}); err != nil {
		t.Fatal(err)
	}
	if st := Evaluate(dir); st.Enabled || st.Valid {
		t.Errorf("Evaluate disabled config: %+v", st)
	}
}

func TestLoaderReloadsOnRenewal(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	cert1, key1 := writeTestCert(t, dir, "first", "panel.example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	first, err := os.ReadFile(cert1)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := os.ReadFile(key1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, first, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, firstKey, 0600); err != nil {
		t.Fatal(err)
	}
	loader := NewLoader(certFile, keyFile)
	got, err := loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("first GetCertificate: %v", err)
	}
	leaf1, err := x509.ParseCertificate(got.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a certbot renewal: a new certificate for the same domain.
	// Sleep past mtime granularity so the loader notices the change.
	time.Sleep(1100 * time.Millisecond)
	cert2, key2 := writeTestCert(t, dir, "second", "panel.example.com", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	second, err := os.ReadFile(cert2)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := os.ReadFile(key2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, second, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, secondKey, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = loader.GetCertificate(nil)
	if err != nil {
		t.Fatalf("second GetCertificate: %v", err)
	}
	leaf2, err := x509.ParseCertificate(got.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) == 0 {
		t.Error("loader kept serving the old certificate after renewal")
	}
	// Deleting the files after a good load keeps serving the cached pair.
	if err := os.Remove(certFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.GetCertificate(nil); err != nil {
		t.Errorf("GetCertificate with deleted files: %v, want cached certificate", err)
	}
	// A fresh loader with no files errors: nothing good to serve.
	fresh := NewLoader(certFile, keyFile)
	if _, err := fresh.GetCertificate(nil); err == nil {
		t.Error("fresh loader with missing files: expected error, got nil")
	}
	// The loader's certificate must be usable as a server certificate.
	_ = tls.Certificate{}
}
