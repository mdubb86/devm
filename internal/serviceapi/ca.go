package serviceapi

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mdubb86/devm/internal/identity"
)

const (
	caRootLifeYrs = 10
	caLeafDaysVal = 90 // leaf cert validity
)

// CA is the daemon's local certificate authority. Root key is
// persisted at LoadOrGenerate time and never moves off-disk except
// for in-process signing. Leaves are signed on demand, cached
// per-hostname.
type CA struct {
	rootCert *x509.Certificate
	rootKey  crypto.Signer
	rootPEM  []byte // PEM-encoded root cert for keychain install

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// LoadOrGenerate returns a CA backed by cfg.RuntimeDir()/ca/.
// Generates on first call; subsequent calls reload the same root.
func LoadOrGenerate(cfg identity.Config) (*CA, error) {
	dir, err := caStorageDir(cfg)
	if err != nil {
		return nil, err
	}
	return loadOrGenerateCAAt(cfg, dir)
}

func caStorageDir(cfg identity.Config) (string, error) {
	dir, err := EnsureRuntimeDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ca"), nil
}

func loadOrGenerateCAAt(cfg identity.Config, dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create ca dir %s: %w", dir, err)
	}
	if ca, err := loadCA(dir); err == nil {
		return ca, nil
	}
	return generateCA(cfg, dir)
}

func loadCA(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, "root.crt"))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "root.key"))
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("ca: invalid root.crt — no PEM block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse root cert: %w", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("ca: invalid root.key — no PEM block")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse root key: %w", err)
	}
	return &CA{
		rootCert: cert,
		rootKey:  key,
		rootPEM:  certPEM,
		cache:    make(map[string]*tls.Certificate),
	}, nil
}

func generateCA(cfg identity.Config, dir string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			CommonName:   cfg.CACommonName(),
			Organization: []string{cfg.Name},
		},
		NotBefore:             now,
		NotAfter:              now.Add(caRootLifeYrs * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create ca cert: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("re-parse ca cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal ca key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(dir, "root.crt"), certPEM, 0644); err != nil {
		return nil, fmt.Errorf("write root.crt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.key"), keyPEM, 0600); err != nil {
		return nil, fmt.Errorf("write root.key: %w", err)
	}

	return &CA{
		rootCert: cert,
		rootKey:  key,
		rootPEM:  certPEM,
		cache:    make(map[string]*tls.Certificate),
	}, nil
}

// RootPEM returns the PEM-encoded root certificate. Used by the
// CLI to call `security add-trusted-cert` during devm install.
func (c *CA) RootPEM() []byte { return c.rootPEM }

// GetCertificate is the tls.Config callback. Signs (and caches)
// a leaf cert for whatever SNI the client requested.
func (c *CA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := hello.ServerName
	if name == "" {
		return nil, errors.New("ca: TLS ClientHello missing SNI")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cert, ok := c.cache[name]; ok {
		// Mid-life renewal: if leaf is within 7 days of expiring,
		// re-sign so long-running daemons never serve an expired cert.
		if time.Until(cert.Leaf.NotAfter) > 7*24*time.Hour {
			return cert, nil
		}
		delete(c.cache, name)
	}
	cert, err := c.signLeaf(name)
	if err != nil {
		return nil, err
	}
	c.cache[name] = cert
	return cert, nil
}

func (c *CA) signLeaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now,
		NotAfter:     now.AddDate(0, 0, caLeafDaysVal),
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.rootCert, &key.PublicKey, c.rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign leaf cert for %s: %w", host, err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("re-parse leaf cert: %w", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        parsed,
	}, nil
}
