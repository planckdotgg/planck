package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"time"

	"github.com/quic-go/quic-go"
)

const ALPN = "planck-spike-v0"

// QUICConfig is the control/datagram connection config shared by both peers.
func QUICConfig() *quic.Config {
	return &quic.Config{
		EnableDatagrams: true,
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: 5 * time.Second,
	}
}

// SelfSignedServerTLS returns a TLS 1.3 server config with a fresh
// ephemeral ECDSA P-256 certificate.
func SelfSignedServerTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "planck-spike"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ALPN},
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}, nil
}

// InsecureClientTLS skips certificate verification. Spike use only: a real
// client pins the host certificate learned at pairing.
func InsecureClientTLS() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{ALPN},
		InsecureSkipVerify: true,
	}
}

// MediaKeysFromConn derives media keys from a QUIC connection's TLS exporter.
func MediaKeysFromConn(c *quic.Conn, suite Suite) (MediaKeys, error) {
	st := c.ConnectionState().TLS
	return DeriveMediaKeys(st.ExportKeyingMaterial, suite)
}
