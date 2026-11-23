// Package discovery finds Planck hosts on the LAN and the tailnet and picks
// the best path to one.
//
// Hosts answer probes and broadcast beacons on one UDP port (DefaultPort).
// Every datagram is a small JSON object tagged with the protocol version.
package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	DefaultPort = 47700

	// SafeShardPayload is the media payload that fits a Tailscale 1280-byte
	// MTU after IPv6, UDP, and Planck's AEAD and media headers. LAN paths use
	// it too: spike 3 measured no gain from larger shards on wired LAN.
	SafeShardPayload = 1152

	protocolVersion = "planck-disc-v1"
	maxMessageLen   = SafeShardPayload
)

type msgType string

const (
	msgBeacon msgType = "beacon"
	msgProbe  msgType = "probe"
	msgReply  msgType = "reply"
)

// Beacon is what a host says about itself.
type Beacon struct {
	HostID          string   `json:"hostID"`
	Name            string   `json:"name"`
	ControlPort     uint16   `json:"controlPort"`
	CertFingerprint string   `json:"certFingerprint"`
	OS              string   `json:"os"`
	GPUs            []string `json:"gpus,omitempty"`
	TailscaleIP     string   `json:"tailscaleIP,omitempty"`
}

type message struct {
	Version string  `json:"v"`
	Type    msgType `json:"t"`
	Nonce   uint64  `json:"n,omitempty"`
	Beacon  *Beacon `json:"b,omitempty"`
}

var errNotPlanck = errors.New("discovery: not a planck-disc-v1 message")

func encode(m message) ([]byte, error) {
	m.Version = protocolVersion
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(b) > maxMessageLen {
		return nil, fmt.Errorf("discovery: %s message is %d bytes, limit %d", m.Type, len(b), maxMessageLen)
	}
	return b, nil
}

func decode(b []byte) (message, error) {
	var m message
	if len(b) > maxMessageLen {
		return m, errNotPlanck
	}
	if err := json.Unmarshal(b, &m); err != nil || m.Version != protocolVersion {
		return m, errNotPlanck
	}
	switch m.Type {
	case msgProbe:
	case msgBeacon, msgReply:
		if m.Beacon == nil || m.Beacon.HostID == "" {
			return m, errNotPlanck
		}
	default:
		return m, errNotPlanck
	}
	return m, nil
}
