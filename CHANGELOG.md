# Changelog

Each release has a section `## [X.Y.Z]`. The Release workflow publishes that section as the release notes and stops when it is missing. Write what a user can do now, one line per change, and name breaking changes first.

## [Unreleased]

## [0.1.0]

The first public release.

- Stream a desktop or a game from a Windows or macOS host to a Windows or macOS client, with hardware encode on NVIDIA (NVENC), AMD (AMF), Intel (oneVPL) and Apple (VideoToolbox) GPUs.
- HEVC, AV1 and H.264, with 4:4:4 chroma and 10-bit HDR10 where the GPU supports them.
- Encrypted low-latency UDP media with adaptive error correction and rate control.
- Find and pair hosts on the local network and on Tailscale; same-user Tailscale devices pair without a PIN.
- Audio, microphone, gamepads, keyboard, mouse and clipboard.
- Windows installer (no administrator rights needed) and portable zip, macOS DMGs for Apple silicon and Intel, and a Linux command-line preview.
