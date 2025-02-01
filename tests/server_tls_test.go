// File: tests/server_tls_test.go
package r_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io/ioutil"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/baditaflorin/r"
)

// generateSelfSignedCert is a helper that returns cert and key PEM bytes.
// (In a real test you might use a library or write a small helper.)
func generateSelfSignedCert() (certPEM, keyPEM []byte, err error) {
	// Generate a new RSA key.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	// Create a serial number for the certificate.
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, nil, err
	}

	// Create the certificate template.
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Example Org"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
	}

	// Self-sign the certificate.
	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	// Encode the certificate to PEM format.
	certPEMBuffer := new(bytes.Buffer)
	if err := pem.Encode(certPEMBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return nil, nil, err
	}

	// Encode the private key to PEM format.
	keyPEMBuffer := new(bytes.Buffer)
	if err := pem.Encode(keyPEMBuffer, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}); err != nil {
		return nil, nil, err
	}

	return certPEMBuffer.Bytes(), keyPEMBuffer.Bytes(), nil
}

func TestServer_TLSConfiguration(t *testing.T) {
	certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		t.Fatalf("Failed to generate self-signed cert: %v", err)
	}

	// Write cert and key to temporary files.
	certFile, err := os.CreateTemp("", "cert.pem")
	if err != nil {
		t.Fatalf("Failed to create temp cert file: %v", err)
	}
	defer os.Remove(certFile.Name())
	keyFile, err := os.CreateTemp("", "key.pem")
	if err != nil {
		t.Fatalf("Failed to create temp key file: %v", err)
	}
	defer os.Remove(keyFile.Name())

	if _, err := certFile.Write(certPEM); err != nil {
		t.Fatalf("Failed to write cert file: %v", err)
	}
	if _, err := keyFile.Write(keyPEM); err != nil {
		t.Fatalf("Failed to write key file: %v", err)
	}
	certFile.Close()
	keyFile.Close()

	// Set up a simple router.
	router := r.NewRouter()
	router.GET("/tls", func(c r.Context) {
		c.String(200, "secure")
	})

	// Configure the server with TLS options.
	config := r.DefaultConfig()
	config.CertFile = certFile.Name()
	config.KeyFile = keyFile.Name()
	config.Handler = router
	server := r.NewServer(config)

	// Start the server in a goroutine.
	go func() {
		if err := server.Start(":8443"); err != nil {
			t.Logf("Server stopped: %v", err)
		}
	}()
	// Allow the server time to start.
	time.Sleep(500 * time.Millisecond)

	// Create an HTTPS client that skips certificate verification.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// InsecureSkipVerify is acceptable here for testing purposes.
				InsecureSkipVerify: true,
				// Optionally, you could load certPEM into a pool:
				RootCAs: func() *x509.CertPool {
					pool := x509.NewCertPool()
					pool.AppendCertsFromPEM(certPEM)
					return pool
				}(),
			},
		},
	}

	resp, err := client.Get("https://localhost:8443/tls")
	if err != nil {
		t.Fatalf("Failed to GET TLS endpoint: %v", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}
	if string(body) != "secure" {
		t.Errorf("Expected response 'secure', got '%s'", string(body))
	}
	if err := server.Stop(); err != nil {
		t.Errorf("Failed to stop server: %v", err)
	}
}
