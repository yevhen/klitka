package daemon

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type mitmCA struct {
	certPath string
	keyPath  string
	cert     *x509.Certificate
	key      *rsa.PrivateKey
	mu       sync.Mutex
	cache    map[string]*tls.Certificate
}

func loadOrCreateCA() (*mitmCA, error) {
	dir, err := mitmDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := readCertificate(certPath)
		if err != nil {
			return nil, err
		}
		key, err := readPrivateKey(keyPath)
		if err != nil {
			return nil, err
		}
		return &mitmCA{certPath: certPath, keyPath: keyPath, cert: cert, key: key, cache: map[string]*tls.Certificate{}}, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	now := time.Now().Add(-1 * time.Hour)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "klitkavm-mitm-ca"},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	return &mitmCA{certPath: certPath, keyPath: keyPath, cert: cert, key: key, cache: map[string]*tls.Certificate{}}, nil
}

func (ca *mitmCA) CertPath() string {
	if ca == nil {
		return ""
	}
	return ca.certPath
}

func (ca *mitmCA) certificateForHost(host string) (*tls.Certificate, error) {
	if ca == nil {
		return nil, fmt.Errorf("mitm CA not initialized")
	}

	normalized := normalizeHost(host)
	ca.mu.Lock()
	if cert, ok := ca.cache[normalized]; ok {
		ca.mu.Unlock()
		return cert, nil
	}
	ca.mu.Unlock()

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	now := time.Now().Add(-1 * time.Hour)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: normalized},
		NotBefore:    now,
		NotAfter:     now.Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	if ip := net.ParseIP(normalized); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{normalized}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}

	cert := &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key}

	ca.mu.Lock()
	ca.cache[normalized] = cert
	ca.mu.Unlock()

	return cert, nil
}

func mitmDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		fallback := filepath.Join(os.TempDir(), "klitkavm-mitm")
		return fallback, nil
	}
	return filepath.Join(configDir, "klitkavm", "mitm"), nil
}

func readCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("invalid cert PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func readPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid key PEM")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		parsed, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("unsupported key type")
		}
		return parsed, nil
	default:
		return nil, fmt.Errorf("unsupported key type: %s", block.Type)
	}
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
