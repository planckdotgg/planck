package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"time"
)

// Clients trust a host by pinning its certificate fingerprint, not by a CA or
// expiry, so the identity is generated once with a long validity.
const identityValidity = 20 * 365 * 24 * time.Hour

// CertFingerprint is the lowercase hex SHA-256 of a DER certificate.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateIdentity loads the host's ECDSA P-256 certificate and key, or
// creates a self-signed pair when neither file exists. It refuses to replace
// a lone cert or key, since that would silently change the pinned identity.
func LoadOrCreateIdentity(certPath, keyPath, commonName string) (tls.Certificate, error) {
	certExists, err := exists(certPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyExists, err := exists(keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	switch {
	case certExists && keyExists:
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("config: load identity: %w", err)
		}
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		return cert, err
	case certExists || keyExists:
		return tls.Certificate{}, fmt.Errorf("config: identity is incomplete: cert %s exists=%v, key %s exists=%v", certPath, certExists, keyPath, keyExists)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(identityValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
