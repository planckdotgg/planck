package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// PairedClientsFile holds the client certificates a host trusts, beside
// host.json.
const PairedClientsFile = "paired-clients.json"

// PairedClient is one client certificate the host trusts.
type PairedClient struct {
	Name     string    `json:"name"`
	Method   string    `json:"method"`
	PairedAt time.Time `json:"pairedAt"`
	// LastSeen is when the client last connected; zero before the first
	// connect after pairing was recorded.
	LastSeen time.Time `json:"lastSeen,omitzero"`
}

// TrustedViaTailnet reports whether the client paired by its Tailscale
// identity rather than a PIN.
func (c PairedClient) TrustedViaTailnet() bool { return strings.HasPrefix(c.Method, "tailscale:") }

// pairings is the host's set of trusted client fingerprints (lowercase hex
// SHA-256), saved as JSON. An empty path keeps it in memory.
type pairings struct {
	mu      sync.Mutex
	path    string
	clients map[string]PairedClient
}

func loadPairings(path string) (*pairings, error) {
	p := &pairings{path: path, clients: map[string]PairedClient{}}
	if path == "" {
		return p, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &p.clients); err != nil {
		return nil, fmt.Errorf("session: %s: %w", path, err)
	}
	if p.clients == nil {
		p.clients = map[string]PairedClient{}
	}
	return p, nil
}

func (p *pairings) lookup(fp [32]byte) (PairedClient, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.clients[hex.EncodeToString(fp[:])]
	return c, ok
}

func (p *pairings) add(fp [32]byte, c PairedClient) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clients[hex.EncodeToString(fp[:])] = c
	return p.saveLocked()
}

// seen records that the client with fp connected at t.
func (p *pairings) seen(fp [32]byte, t time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := hex.EncodeToString(fp[:])
	c, ok := p.clients[key]
	if !ok {
		return nil
	}
	c.LastSeen = t
	p.clients[key] = c
	return p.saveLocked()
}

// remove forgets the clients whose fingerprints (lowercase hex) are in
// fps, or every client when fps is nil, and returns the ones it forgot.
func (p *pairings) remove(fps []string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var gone []string
	for key := range p.clients {
		if fps == nil || slices.Contains(fps, key) {
			gone = append(gone, key)
		}
	}
	if len(gone) == 0 {
		return nil, nil
	}
	for _, key := range gone {
		delete(p.clients, key)
	}
	return gone, p.saveLocked()
}

func (p *pairings) saveLocked() error {
	if p.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(p.clients, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p.path, append(b, '\n'))
}

func (p *pairings) list() map[string]PairedClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]PairedClient, len(p.clients))
	for k, v := range p.clients {
		out[k] = v
	}
	return out
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// newPIN returns a uniformly random 6-digit PIN.
func newPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
