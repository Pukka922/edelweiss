package tlsutil

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestEnsureSelfSigned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	certFile, keyFile, created, err := EnsureSelfSigned(dir, []string{"alpine.lan", "192.168.1.5"}, now)
	if err != nil || !created {
		t.Fatalf("first call: created %v, %v", created, err)
	}
	if fi, _ := os.Stat(keyFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", fi.Mode().Perm())
	}
	cert := parseCert(t, certFile)
	if !slices.Contains(cert.DNSNames, "alpine.lan") || !slices.Contains(cert.DNSNames, "localhost") {
		t.Fatalf("dns names %v", cert.DNSNames)
	}
	if !slices.ContainsFunc(cert.IPAddresses, net.ParseIP("192.168.1.5").Equal) {
		t.Fatalf("ips %v", cert.IPAddresses)
	}
	if !cert.NotAfter.Equal(now.Add(365 * 24 * time.Hour).Truncate(time.Second)) {
		t.Fatalf("not after %v", cert.NotAfter)
	}

	first, _ := os.ReadFile(certFile)
	if _, _, created, err := EnsureSelfSigned(dir, nil, now.Add(24*time.Hour)); err != nil || created {
		t.Fatalf("valid certificate: created %v, %v", created, err)
	}
	second, _ := os.ReadFile(certFile)
	if !bytes.Equal(first, second) {
		t.Fatal("valid certificate was regenerated")
	}

	if _, _, created, err := EnsureSelfSigned(dir, nil, now.Add(340*24*time.Hour)); err != nil || !created {
		t.Fatalf("expiring certificate: created %v, %v", created, err)
	}
	third, _ := os.ReadFile(certFile)
	if bytes.Equal(first, third) {
		t.Fatal("expiring certificate was not renewed")
	}
}

// The directory holds the private key: a directory that already exists with a
// wider mode (created by hand, restored from a backup) is tightened as well.
func TestEnsureSelfSignedTightensAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { // whatever the umask
		t.Fatal(err)
	}
	if _, _, _, err := EnsureSelfSigned(dir, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("tls directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
	}
}

func TestLocalHostsIncludesHostname(t *testing.T) {
	h, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname")
	}
	if !slices.Contains(LocalHosts(), h) {
		t.Fatalf("LocalHosts() missing %q", h)
	}
}
