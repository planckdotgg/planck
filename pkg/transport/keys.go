package transport

import "fmt"

// MediaExporterLabel is the RFC 8446 §7.5 exporter label for media keys.
const MediaExporterLabel = "EXPORTER-planck-media-v0"

// Exporter matches tls.ConnectionState.ExportKeyingMaterial.
type Exporter func(label string, context []byte, length int) ([]byte, error)

// MediaKeys holds one key per direction. Client is the peer that dialed the
// control connection (the streaming host).
type MediaKeys struct {
	ClientToServer DirectionKey
	ServerToClient DirectionKey
}

// DeriveMediaKeys derives directional media keys from the TLS 1.3 exporter of
// the control connection. Both peers derive identical keys; the suite is bound
// into the exporter context so a suite mismatch yields unrelated keys.
func DeriveMediaKeys(export Exporter, suite Suite) (MediaKeys, error) {
	kl := suite.keyLen()
	if kl == 0 {
		return MediaKeys{}, fmt.Errorf("transport: unsupported suite %v", suite)
	}
	material, err := export(MediaExporterLabel, []byte{byte(suite)}, 2*(kl+ivLen))
	if err != nil {
		return MediaKeys{}, fmt.Errorf("transport: export keying material: %w", err)
	}
	split := func(b []byte) DirectionKey {
		k := DirectionKey{Suite: suite, Key: b[:kl:kl]}
		copy(k.IV[:], b[kl:kl+ivLen])
		return k
	}
	return MediaKeys{
		ClientToServer: split(material[:kl+ivLen]),
		ServerToClient: split(material[kl+ivLen:]),
	}, nil
}
