# Changelog

Each release has a section `## [X.Y.Z]`. The Release workflow publishes that section as the release notes and stops when it is missing. Write what a user can do now, one line per change, and name breaking changes first.

## [Unreleased]

## [0.2.0]

- Breaking: default ports are now UDP 41000 (discovery), 41001 (QUIC control) and 41002 (media). The old 47700-47702 fall in the range WSL's mirrored networking reserves on Windows, where the host could not start. A `host.json` that still holds the old defaults moves to the new ones; custom ports stay. Update host and client together: older clients listen for hosts on 47700.
- Linux desktop client and host (beta): full desktop `planck-client` GUI and standalone `planck-host` with X11 MIT-SHM and Wayland ScreenCast/PipeWire capture, NVIDIA NVENC and VA-API HEVC encode, audio, input, `.desktop` entry, `planck-host.service`, and `install.sh` (x86_64 and aarch64 tarballs). Newly shipped and CI-tested — Linux hardware feedback is welcome.
- Auto-tune: in-app auto-configurator that benchmarks codec, slice count, VSync and bitrate for each host.
- VSync phase-locking (`-phase-lock`) to align frame presentation with the display's scan-out grid and cut pacing jitter.
- Windows UDP GSO send batching and macOS receive-queue pacing for smoother high-bitrate bursts on LAN.
- Discovery and recovery fixes: a host on a custom discovery port also announces on the default port, and a lost frame reported while waiting for the display recovers on the next picture via reference invalidation instead of falling back to a keyframe a frame later.
- Single desktop download per platform runs both client and built-in host (via the in-app Hosting toggle), with standalone `planck-host` included for headless or background-service hosting.

## [0.1.0]

The first public release.

- Stream a desktop or a game from a Windows or macOS host to a Windows or macOS client, with hardware encode on NVIDIA (NVENC), AMD (AMF), Intel (oneVPL) and Apple (VideoToolbox) GPUs.
- HEVC, AV1 and H.264, with 4:4:4 chroma and 10-bit HDR10 where the GPU supports them.
- Encrypted low-latency UDP media with adaptive error correction and rate control.
- Find and pair hosts on the local network and on Tailscale; same-user Tailscale devices pair without a PIN.
- Audio, microphone, gamepads, keyboard, mouse and clipboard.
- Windows installer (no administrator rights needed) and portable zip, macOS DMGs for Apple silicon and Intel, and a Linux command-line preview.
- Anonymous usage telemetry through TelemetryDeck: install, launch and stream counts with the codec and route type, under a hashed random ID, and never names, addresses, screen content, audio, input or PINs. It is on by default; turn it off in Settings › General, or with `planck-host -no-telemetry`. See [Privacy](README.md#privacy-anonymous-usage-telemetry).
