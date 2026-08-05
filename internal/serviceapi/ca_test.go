package serviceapi

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mdubb86/devm/internal/identity"
)

func TestCA_GenerateThenLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	ca1, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)
	assert.NotEmpty(t, ca1.RootPEM())

	ca2, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)
	assert.Equal(t, ca1.RootPEM(), ca2.RootPEM(),
		"second load must reuse the persisted root, not regenerate")
}

func TestCA_SignLeaf_VerifiesAgainstRoot(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	cert, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.test"})
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.Len(t, cert.Certificate, 1)

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)

	roots := x509.NewCertPool()
	roots.AddCert(ca.rootCert)

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "app.test",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	assert.NoError(t, err, "leaf must verify against the CA root we just generated")
}

func TestCA_GetCertificate_RequiresSNI(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	_, err = ca.GetCertificate(&tls.ClientHelloInfo{ServerName: ""})
	require.Error(t, err)
}

func TestCA_LeafCache_ReusesSignedCert(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	c1, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.test"})
	require.NoError(t, err)
	c2, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "app.test"})
	require.NoError(t, err)

	assert.Same(t, c1, c2)
}

// TestCA_LeafCache_BoundedUnderManyDistinctSNIs covers F6: guest-origin
// listeners let guest-originated traffic drive GetCertificate through
// arbitrary SNIs, so the leaf cache must never grow past its cap no
// matter how many distinct hostnames are requested.
func TestCA_LeafCache_BoundedUnderManyDistinctSNIs(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	for i := 0; i < caLeafCacheMax*2; i++ {
		host := fmt.Sprintf("host-%d.test", i)
		_, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		require.NoError(t, err)

		ca.mu.Lock()
		size := len(ca.cache)
		ca.mu.Unlock()
		require.LessOrEqual(t, size, caLeafCacheMax, "cache must never exceed its cap")
	}
}

// TestCA_LeafCache_ServesHostAfterUnrelatedEvictions pins that a host
// requested before a wave of evictions is still servable afterward —
// either because it survived (arbitrary eviction can spare it) or
// because GetCertificate transparently re-signs it. Either way the
// caller must get back a cert that verifies against this CA's root.
func TestCA_LeafCache_ServesHostAfterUnrelatedEvictions(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	_, err = ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "keep.test"})
	require.NoError(t, err)

	// Churn enough distinct SNIs to force the cap to evict repeatedly.
	for i := 0; i < caLeafCacheMax*2; i++ {
		host := fmt.Sprintf("churn-%d.test", i)
		_, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		require.NoError(t, err)
	}

	again, err := ca.GetCertificate(&tls.ClientHelloInfo{ServerName: "keep.test"})
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(again.Certificate[0])
	require.NoError(t, err)

	roots := x509.NewCertPool()
	roots.AddCert(ca.rootCert)
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   "keep.test",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	assert.NoError(t, err, "keep.test must still verify whether cached or re-signed")
}

func TestCA_RootKey_Persisted0600(t *testing.T) {
	dir := t.TempDir()
	_, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, "root.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

// TestCA_RootCertSubject_ScopedByProfile pins that the generated root
// cert's CommonName and Organization are derived from cfg — under E2E
// they must not read "devm"/"devm Local CA" (prod's values), so the
// system keychain can tell the two profiles' trust chains apart.
func TestCA_RootCertSubject_ScopedByProfile(t *testing.T) {
	prodCA, err := loadOrGenerateCAAt(identity.Prod, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "devm Local CA", prodCA.rootCert.Subject.CommonName)
	assert.Equal(t, []string{"devm"}, prodCA.rootCert.Subject.Organization)

	e2eCA, err := loadOrGenerateCAAt(identity.E2E, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "devm-e2e Local CA", e2eCA.rootCert.Subject.CommonName)
	assert.Equal(t, []string{"devm-e2e"}, e2eCA.rootCert.Subject.Organization)
}

func TestCA_RootCertValidity_IsTenYears(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrGenerateCAAt(identity.Prod, dir)
	require.NoError(t, err)

	expectedLifetime := 10 * 365 * 24 * time.Hour
	actual := ca.rootCert.NotAfter.Sub(ca.rootCert.NotBefore)
	tolerance := 24 * time.Hour
	assert.InDelta(t, expectedLifetime.Hours(), actual.Hours(), tolerance.Hours(),
		"root cert lifetime should be ~10 years (mkcert convention)")
}
