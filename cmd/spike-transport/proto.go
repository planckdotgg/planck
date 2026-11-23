package main

import (
	"fmt"
	"strconv"
	"strings"
)

// RunConfig is sent by the host (sender) to the client (receiver) on the
// control stream before each run.
type RunConfig struct {
	Name           string  `json:"name"`
	Path           string  `json:"path"`
	Transport      string  `json:"transport"`
	Suite          string  `json:"suite"`
	BitrateMbps    float64 `json:"bitrate_mbps"`
	FPS            int     `json:"fps"`
	Slices         int     `json:"slices"`
	FEC            string  `json:"fec"`
	Loss           float64 `json:"loss"`
	JitterMs       float64 `json:"jitter_ms"`
	PaceMbps       float64 `json:"pace_mbps"`
	ShardSize      int     `json:"shard_size"`
	QUICPacketSize int     `json:"quic_packet_size"`
	DurationMs     int     `json:"duration_ms"`
	WarmupMs       int     `json:"warmup_ms"`
}

func (c RunConfig) label() string {
	tr := c.Transport
	if tr == "raw" {
		if c.PaceMbps > 0 {
			tr = fmt.Sprintf("raw/pace%.0f", c.PaceMbps)
		} else {
			tr = "raw/burst"
		}
	}
	imp := fmt.Sprintf("%g%%", c.Loss*100)
	if c.JitterMs > 0 {
		imp += fmt.Sprintf("+%gms", c.JitterMs)
	}
	return fmt.Sprintf("%s %s %3.0fMb@%d s%d fec%s loss%s sh%d",
		c.Path, tr, c.BitrateMbps, c.FPS, c.Slices, c.FEC, imp, c.ShardSize)
}

// fecPercent returns the fixed parity percentage, or -1 for adaptive.
func (c RunConfig) fecPercent() (int, error) {
	if c.FEC == "adaptive" {
		return -1, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(c.FEC, "%"))
	if err != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("bad fec %q", c.FEC)
	}
	return n, nil
}

// RxSummary is the receiver's report for one run.
type RxSummary struct {
	Packets          uint64  `json:"packets"`
	MediaBytes       uint64  `json:"media_bytes"`
	ActiveSec        float64 `json:"active_sec"`
	AuthFail         uint64  `json:"auth_fail"`
	Invalid          uint64  `json:"invalid"`
	Late             uint64  `json:"late"`
	FramesComplete   uint64  `json:"frames_complete"`
	FramesIncomplete uint64  `json:"frames_incomplete"`
	SlicesRecovered  uint64  `json:"slices_recovered"`
	SlicesLost       uint64  `json:"slices_lost"`
	RecvShards       uint64  `json:"recv_shards"`
	SentShards       uint64  `json:"sent_shards"`
	Mallocs          uint64  `json:"mallocs"`
	NumGC            uint32  `json:"num_gc"`
	Error            string  `json:"error,omitempty"`
}
