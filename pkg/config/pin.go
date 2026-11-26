package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUnknownHost         = errors.New("config: host is not paired")
	ErrFingerprintMismatch = errors.New("config: host certificate does not match the pinned fingerprint")
)

type PinnedHost struct {
	HostID          string    `json:"hostID"`
	Name            string    `json:"name"`
	CertFingerprint string    `json:"certFingerprint"`
	PairedAt        time.Time `json:"pairedAt"`
	LastAddr        string    `json:"lastAddr,omitempty"`
	// LastLANAddr and LastTailscaleAddr are the host's control addresses
	// on each kind of route it was last reached or found on.
	LastLANAddr       string `json:"lastLANAddr,omitempty"`
	LastTailscaleAddr string `json:"lastTailscaleAddr,omitempty"`
	// Calibrated is what calibration kept for the host, by route: a
	// discovery.PathKind's String.
	Calibrated map[string]CalibratedRoute `json:"calibrated,omitempty"`
}

// Pin records a paired host, replacing any earlier pin for the same HostID.
func (c *ClientConfig) Pin(h PinnedHost) error {
	if h.HostID == "" || len(h.CertFingerprint) != 64 {
		return fmt.Errorf("config: pin needs a hostID and a SHA-256 fingerprint, got %q / %q", h.HostID, h.CertFingerprint)
	}
	h.CertFingerprint = strings.ToLower(h.CertFingerprint)
	if c.PinnedHosts == nil {
		c.PinnedHosts = map[string]PinnedHost{}
	}
	c.PinnedHosts[h.HostID] = h
	return nil
}

// CheckPin reports whether fingerprint is the one pinned for hostID.
func (c ClientConfig) CheckPin(hostID, fingerprint string) (PinnedHost, error) {
	h, ok := c.PinnedHosts[hostID]
	if !ok {
		return PinnedHost{}, fmt.Errorf("%w: %s", ErrUnknownHost, hostID)
	}
	if !strings.EqualFold(h.CertFingerprint, fingerprint) {
		return h, fmt.Errorf("%w: %s", ErrFingerprintMismatch, hostID)
	}
	return h, nil
}

// VerifyPinnedCert returns a tls.Config.VerifyPeerCertificate callback that
// accepts only a leaf certificate with the given fingerprint. Use it with
// InsecureSkipVerify, which only turns off CA and hostname checks.
func VerifyPinnedCert(fingerprint string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("config: peer sent no certificate")
		}
		if !strings.EqualFold(CertFingerprint(rawCerts[0]), fingerprint) {
			return ErrFingerprintMismatch
		}
		return nil
	}
}
