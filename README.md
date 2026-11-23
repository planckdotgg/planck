# Planck

Ultra-low-latency, open-source game streaming in pure Go and [MyGo](https://github.com/egoist/mygo).

- **Website:** [planck.gg](https://planck.gg)
- **License:** Apache-2.0

## Vision

Planck is a single-binary host and native client for local and remote game streaming — built to compete head-to-head with Sunshine/Moonlight and Parsec without C++/Qt bloat or proprietary lock-in.

- **Pure Go core** (`CGO_ENABLED=0` where possible, zero Rust)
- **Vendored MyGo UI** with direct GPU surface presentation, HDR, and 4:4:4 chroma support
- **Hardware-accelerated capture & encode** (DXGI Desktop Duplication + NVENC/AMF/QSV on Windows; ScreenCaptureKit + VideoToolbox on macOS; PipeWire/KMS + VAAPI/NVENC on Linux)
- **QUIC datagram transport** with adaptive FEC, pacing, and low-latency input

## Status

Early development — Phase 0 (zero-copy 240 fps surface, pure-Go hardware encode, and QUIC datagram FEC spikes).
