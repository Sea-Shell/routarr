package tlsutil_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bateau84/routarr/internal/tlsutil"
)

func TestGenerateSelfSignedCert(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")

	if err := tlsutil.GenerateSelfSignedCert(certFile, keyFile); err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}

	// --- files exist and have restricted permissions ---
	for _, path := range []string{certFile, keyFile} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Errorf("%s: want mode 0600, got %04o", path, fi.Mode().Perm())
		}
	}

	// --- cert is valid PEM and parses as x509.Certificate ---
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("cert: no PEM block found")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("cert PEM type: want CERTIFICATE, got %s", block.Type)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	// --- validity window ---
	now := time.Now()
	if now.Before(cert.NotBefore) {
		t.Errorf("certificate NotBefore %v is in the future", cert.NotBefore)
	}
	if now.After(cert.NotAfter) {
		t.Errorf("certificate has already expired at %v", cert.NotAfter)
	}

	wantDuration := 365 * 24 * time.Hour
	gotDuration := cert.NotAfter.Sub(cert.NotBefore)
	// Allow a few seconds of drift from test execution time.
	if gotDuration < wantDuration-10*time.Second || gotDuration > wantDuration+10*time.Second {
		t.Errorf("certificate validity: want ~%v, got %v", wantDuration, gotDuration)
	}

	// --- required DNS SAN ---
	wantDNS := "localhost"
	found := false
	for _, dns := range cert.DNSNames {
		if dns == wantDNS {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DNS SAN %q not found in cert.DNSNames %v", wantDNS, cert.DNSNames)
	}

	// --- required IP SANs ---
	wantIPs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, want := range wantIPs {
		found := false
		for _, ip := range cert.IPAddresses {
			if ip.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("IP SAN %s not found in cert.IPAddresses %v", want, cert.IPAddresses)
		}
	}

	// --- ExtKeyUsage includes ServerAuth ---
	hasServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
			break
		}
	}
	if !hasServerAuth {
		t.Errorf("certificate missing ExtKeyUsageServerAuth; got %v", cert.ExtKeyUsage)
	}

	// --- key file is valid PEM ---
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("key: no PEM block found")
	}
	if keyBlock.Type != "EC PRIVATE KEY" {
		t.Errorf("key PEM type: want EC PRIVATE KEY, got %s", keyBlock.Type)
	}

	// --- cert + key can be loaded together as a valid TLS identity ---
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Errorf("tls.LoadX509KeyPair: %v", err)
	}
}

func TestGenerateSelfSignedCert_BadCertPath(t *testing.T) {
	if err := tlsutil.GenerateSelfSignedCert("/nonexistent/path/cert.pem", "/tmp/key.pem"); err == nil {
		t.Error("expected error for unwritable cert path, got nil")
	}
}

func TestGenerateSelfSignedCert_BadKeyPath(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")

	if err := tlsutil.GenerateSelfSignedCert(certFile, "/nonexistent/path/key.pem"); err == nil {
		t.Error("expected error for unwritable key path, got nil")
	}
}
