package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/planckdotgg/planck/pkg/wire"
)

// ControlALPN is the ALPN protocol of the production control connection.
const ControlALPN = "planck-v1"

const (
	mediaExporterLabelV1 = "EXPORTER-planck-media-v1"
	pairExporterLabel    = "EXPORTER-planck-pair-v1"
)

var ErrHostNotTrusted = errors.New("transport: host certificate not trusted")

// NewIdentity creates a long-lived self-signed ECDSA P-256 certificate that
// identifies a host or client. Peers trust it by its SHA-256 fingerprint,
// learned at pairing, not by a CA.
func NewIdentity(name string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// Fingerprint is the SHA-256 of a DER certificate.
func Fingerprint(der []byte) [32]byte { return sha256.Sum256(der) }

// CertFingerprint is the fingerprint of a certificate's leaf.
func CertFingerprint(c tls.Certificate) [32]byte {
	if len(c.Certificate) == 0 {
		return [32]byte{}
	}
	return Fingerprint(c.Certificate[0])
}

// HostVerifier decides whether to trust a host by its certificate
// fingerprint. It replaces CA verification on the client.
type HostVerifier func(fingerprint [32]byte) error

// PinnedHost trusts exactly one host certificate.
func PinnedHost(want [32]byte) HostVerifier {
	return func(got [32]byte) error {
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			return ErrHostNotTrusted
		}
		return nil
	}
}

// ServerTLSConfig is the host's control-connection TLS config. Clients must
// present a certificate; its fingerprint is their identity.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{ControlALPN},
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
	}
}

// ClientTLSConfig is the client's control-connection TLS config. Hosts use
// self-signed certificates, so CA chain verification is replaced, not
// skipped: Go only allows that through InsecureSkipVerify, and
// VerifyConnection, which runs regardless, enforces verify on every
// handshake.
func ClientTLSConfig(cert tls.Certificate, verify HostVerifier) (*tls.Config, error) {
	if verify == nil {
		return nil, errors.New("transport: ClientTLSConfig needs a HostVerifier")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{ControlALPN},
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrHostNotTrusted
			}
			return verify(Fingerprint(cs.PeerCertificates[0].Raw))
		},
	}, nil
}

func controlQUICConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:  30 * time.Second,
		KeepAlivePeriod: 5 * time.Second,
	}
}

// pinProof binds a pairing PIN to this TLS session and to the prover's
// role, so it cannot be replayed on another connection or reflected back.
func pinProof(export Exporter, pin string, role wire.Role) ([32]byte, error) {
	var out [32]byte
	ekm, err := export(pairExporterLabel, nil, 32)
	if err != nil {
		return out, fmt.Errorf("transport: export pairing binding: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(pin))
	mac.Write([]byte{byte(role)})
	mac.Write(ekm)
	copy(out[:], mac.Sum(nil))
	return out, nil
}

func proofEqual(a, b [32]byte) bool { return subtle.ConstantTimeCompare(a[:], b[:]) == 1 }

func deriveMediaKeysV1(export Exporter) (MediaKeys, error) {
	return deriveMediaKeysLabel(export, SuiteAES128GCM, mediaExporterLabelV1)
}
