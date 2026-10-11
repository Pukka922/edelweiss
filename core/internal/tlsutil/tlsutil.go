// Package tlsutil creates the self-signed certificate used when no
// certificate is configured.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	validity    = 365 * 24 * time.Hour
	renewBefore = 30 * 24 * time.Hour
)

// EnsureSelfSigned returns cert/key paths in dir, creating or renewing a
// self-signed certificate when none exists or it expires within 30 days;
// created reports whether it did. dir (which holds the private key) is created
// or tightened to mode 0700.
func EnsureSelfSigned(dir string, hosts []string, now time.Time) (certFile, keyFile string, created bool, err error) {
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", false, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", "", false, err
	}
	if usable(certFile, keyFile, now) {
		return certFile, keyFile, false, nil
	}
	if err := generate(certFile, keyFile, hosts, now); err != nil {
		return "", "", false, fmt.Errorf("generate self-signed certificate: %w", err)
	}
	return certFile, keyFile, true, nil
}

func usable(certFile, keyFile string, now time.Time) bool {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return false
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return false
	}
	return now.Add(renewBefore).Before(cert.NotAfter)
}

func generate(certFile, keyFile string, hosts []string, now time.Time) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName(hosts), Organization: []string{"edelweiss (self-signed)"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range append(slices.Clone(hosts), "localhost", "127.0.0.1", "::1") {
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			if !slices.ContainsFunc(tmpl.IPAddresses, ip.Equal) {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			}
		} else if !slices.Contains(tmpl.DNSNames, h) {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeAtomic(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return writeAtomic(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func commonName(hosts []string) string {
	for _, h := range hosts {
		if h != "" && net.ParseIP(h) == nil {
			return h
		}
	}
	return "edelweiss"
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LocalHosts returns the hostname and the non-loopback IP addresses of this host.
func LocalHosts() []string {
	var hosts []string
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			hosts = append(hosts, ipn.IP.String())
		}
	}
	return hosts
}
