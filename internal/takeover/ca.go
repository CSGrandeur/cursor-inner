package takeover

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"cursor-inner/internal/fsutil"
)

func CertPaths(dir string) (certPath, keyPath string) {
	base := filepath.Join(dir, "ca")
	return filepath.Join(base, "ca.crt"), filepath.Join(base, "ca.key")
}

func EnsureCA(dir string) (tls.Certificate, error) {
	certPath, keyPath := CertPaths(dir)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil && leaf.IsCA && time.Now().Before(leaf.NotAfter) {
			cert.Leaf = leaf
			return cert, nil
		}
	}
	return generateCA(certPath, keyPath)
}

func generateCA(certPath, keyPath string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cursor-inner Local CA", Organization: []string{"cursor-inner"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := fsutil.WriteFile(certPath, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	if err := fsutil.WriteFile(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	_ = os.Chmod(keyPath, 0o600)
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func LoadLeaf(dir string) (*x509.Certificate, error) {
	certPath, _ := CertPaths(dir)
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("证书文件无法解析")
	}
	return x509.ParseCertificate(block.Bytes)
}
