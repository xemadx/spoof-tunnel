// Package tlsconf owns the panel's TLS configuration: a small tls.json file
// in the panel data directory, validation of a certificate/key pair for one
// domain, and a GetCertificate loader that re-reads the files when they
// change, so a certbot renewal takes effect with no panel restart.
//
// Safety rule: a broken TLS configuration must never brick the panel. The
// loader keeps serving the last good certificate when a reload fails, and
// the serve path falls back to plain HTTP when no valid certificate exists
// at startup. The installer is what turns TLS on; this package only reads
// what the installer wrote.
package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Config is the on-disk TLS configuration (tls.json, mode 0600).
type Config struct {
	Enabled  bool   `json:"enabled"`
	Domain   string `json:"domain"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// Status is the evaluated state of the configuration: what the panel will
// actually serve. Valid is true only when TLS is enabled and the files on
// disk load, match each other, cover the domain and are currently in date.
type Status struct {
	Enabled  bool
	Domain   string
	CertFile string
	KeyFile  string
	Expires  time.Time
	Issuer   string
	Valid    bool
	Error    string
}

// Path returns the tls.json path for a data directory.
func Path(dataDir string) string {
	return filepath.Join(dataDir, "tls.json")
}

// domainPattern is a strict FQDN: labels of letters/digits/hyphens, dots
// between them, a TLD of at least two letters. Wildcards are rejected:
// the installer obtains certificates with the HTTP-01 challenge, which
// cannot prove a wildcard.
var domainPattern = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,}$`)

// ValidDomain reports whether domain is a usable FQDN for a certificate.
func ValidDomain(domain string) bool {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if len(domain) == 0 || len(domain) > 253 || strings.Contains(domain, "*") {
		return false
	}
	return domainPattern.MatchString(domain)
}

// NormalizeDomain lower-cases a domain and drops one trailing dot.
func NormalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// Load reads tls.json. A missing file means TLS was never configured and is
// not an error. A present but unreadable file is an error: silently
// ignoring a broken configuration would hide exactly the failure the
// operator needs to see.
func Load(dataDir string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(Path(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid tls.json: %w", err)
	}
	cfg.Domain = NormalizeDomain(cfg.Domain)
	return cfg, nil
}

// Save writes tls.json atomically (temp file + rename) with mode 0600.
func Save(dataDir string, cfg Config) error {
	cfg.Domain = NormalizeDomain(cfg.Domain)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dataDir, ".tls-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, Path(dataDir)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Validate loads a certificate/key pair and checks it for one domain.
// It returns the leaf certificate's expiry and issuer. The checks are, in
// order: both files parse and the key matches the certificate; the leaf is
// currently in date; the leaf covers the domain (SAN DNS names, or the
// legacy CN when the certificate carries no SANs at all).
func Validate(certFile, keyFile, domain string) (expires time.Time, issuer string, err error) {
	domain = NormalizeDomain(domain)
	if !ValidDomain(domain) {
		return time.Time{}, "", fmt.Errorf("invalid domain %q", domain)
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("certificate/key pair does not load: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return time.Time{}, "", errors.New("certificate file holds no certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("certificate does not parse: %w", err)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return time.Time{}, "", fmt.Errorf("certificate is not valid before %s", leaf.NotBefore.Format(time.RFC3339))
	}
	if now.After(leaf.NotAfter) {
		return time.Time{}, "", fmt.Errorf("certificate expired on %s", leaf.NotAfter.Format(time.RFC3339))
	}
	covered := false
	for _, name := range leaf.DNSNames {
		if strings.EqualFold(name, domain) {
			covered = true
			break
		}
	}
	if !covered && len(leaf.DNSNames) == 0 && strings.EqualFold(leaf.Subject.CommonName, domain) {
		covered = true
	}
	if !covered {
		return time.Time{}, "", fmt.Errorf("certificate does not cover %q (SANs: %s)", domain, strings.Join(leaf.DNSNames, ", "))
	}
	return leaf.NotAfter, leaf.Issuer.String(), nil
}

// Evaluate evaluates the configuration on disk. It never returns an error:
// every failure mode is reported in the Status itself, so callers can
// decide between HTTPS, HTTP fallback and asking the operator.
func Evaluate(dataDir string) Status {
	cfg, err := Load(dataDir)
	if err != nil {
		return Status{Error: err.Error()}
	}
	st := Status{Enabled: cfg.Enabled, Domain: cfg.Domain, CertFile: cfg.CertFile, KeyFile: cfg.KeyFile}
	if !cfg.Enabled {
		return st
	}
	expires, issuer, err := Validate(cfg.CertFile, cfg.KeyFile, cfg.Domain)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Expires = expires
	st.Issuer = issuer
	st.Valid = true
	return st
}

// Loader is a tls.Config GetCertificate callback that re-reads the
// certificate and key from disk whenever either file changes, so a certbot
// renewal is picked up by the next handshake with no restart. A failed
// reload keeps serving the last good certificate instead of breaking
// handshakes; only a load with no previously good certificate errors.
type Loader struct {
	certFile string
	keyFile  string
	mu       sync.RWMutex
	cert     *tls.Certificate
	certMod  time.Time
	keyMod   time.Time
}

// NewLoader returns a loader for a certificate/key pair. Nothing is read
// until the first handshake.
func NewLoader(certFile, keyFile string) *Loader {
	return &Loader{certFile: certFile, keyFile: keyFile}
}

// GetCertificate implements tls.Config.GetCertificate.
func (l *Loader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	l.mu.RLock()
	cached, certMod, keyMod := l.cert, l.certMod, l.keyMod
	l.mu.RUnlock()

	certInfo, err := os.Stat(l.certFile)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("certificate file unreadable: %w", err)
	}
	keyInfo, err := os.Stat(l.keyFile)
	if err != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("key file unreadable: %w", err)
	}
	if cached != nil && certInfo.ModTime().Equal(certMod) && keyInfo.ModTime().Equal(keyMod) {
		return cached, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// Another goroutine may have reloaded while we waited.
	if l.cert != nil && certInfo.ModTime().Equal(l.certMod) && keyInfo.ModTime().Equal(l.keyMod) {
		return l.cert, nil
	}
	pair, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		if l.cert != nil {
			return l.cert, nil
		}
		return nil, fmt.Errorf("certificate/key pair does not load: %w", err)
	}
	l.cert = &pair
	l.certMod = certInfo.ModTime()
	l.keyMod = keyInfo.ModTime()
	return l.cert, nil
}
