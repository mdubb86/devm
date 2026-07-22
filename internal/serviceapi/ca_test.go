package serviceapi

import (
	"crypto/tls"
	"crypto/x509"
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
