package server

// HTTPS termination for forwarded ports. An image preset can opt a container
// port into an HTTPS-terminated forward (see store.ImagePreset.HTTPS8080/
// HTTPS8090): mudp itself owns the host socket and speaks TLS on it, then
// relays the decrypted bytes to the container in plaintext, so the container
// image never has to carry its own certificate.
//
// There is no public hostname to get a CA-signed certificate for — a forwarded
// port is reached by host or LAN IP — so mudp generates and persists a
// self-signed one on first use. A browser will show a one-time trust warning
// for it, the same as any self-hosted device's default HTTPS page.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// forwardCertLifetime is how long the self-signed certificate stays valid.
// Long enough that an operator never has to think about renewal.
const forwardCertLifetime = 10 * 365 * 24 * time.Hour

// setupForwardTLS installs the certificate used to terminate HTTPS on
// forwards an image preset opted into. A failure to load or generate one
// (e.g. an unwritable data directory) only disables HTTPS forwarding — every
// other forward, and the console itself, is unaffected — so it is logged
// rather than returned as a startup error.
func (a *App) setupForwardTLS() {
	certPath, keyPath := forwardCertPaths(a.cfg.DBPath)
	var cert tls.Certificate
	var err error
	if certPath == "" {
		// No configured database path to anchor persistence to (e.g. a test
		// harness that never sets DBPath). Generate an ephemeral certificate for
		// this run rather than writing into the process's working directory.
		var certPEM, keyPEM []byte
		if certPEM, keyPEM, err = generateSelfSignedCert(); err == nil {
			cert, err = tls.X509KeyPair(certPEM, keyPEM)
		}
	} else {
		cert, err = loadOrCreateForwardCert(certPath, keyPath)
	}
	if err != nil {
		log.Printf("https forwarding disabled: %v", err)
		return
	}
	a.forward.SetTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}})
}

// forwardCertPaths derives the cert/key file locations from the database
// path, so they live alongside it without needing a new config option. An
// empty dbPath (no real database file to anchor to) yields empty paths,
// signalling the caller to skip disk persistence entirely.
func forwardCertPaths(dbPath string) (certPath, keyPath string) {
	if strings.TrimSpace(dbPath) == "" {
		return "", ""
	}
	dir := filepath.Dir(dbPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "mudp-forward-cert.pem"), filepath.Join(dir, "mudp-forward-key.pem")
}

// loadOrCreateForwardCert loads a previously generated cert+key pair, or
// generates and persists a new self-signed one on first use.
func loadOrCreateForwardCert(certPath, keyPath string) (tls.Certificate, error) {
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}
	certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		return tls.Certificate{}, err
	}
	// The key first, and with restrictive permissions, so a crash between the
	// two writes never leaves a world-readable private key on disk.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// generateSelfSignedCert creates a fresh EC keypair and a self-signed
// certificate for it, covering localhost plus every non-loopback IP this host
// currently has — so a browser reaching a forwarded port by LAN IP gets a
// certificate whose Subject Alternative Names actually match, and only has to
// accept the "self-signed" warning once rather than also a hostname mismatch.
func generateSelfSignedCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	ips := append([]net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, hostIPs()...)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "mudp forwarded port"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(forwardCertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// hostIPs lists this host's non-loopback, non-link-local unicast addresses,
// for embedding in the self-signed cert's SAN list.
func hostIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var ips []net.IP
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		ips = append(ips, ip)
	}
	return ips
}
