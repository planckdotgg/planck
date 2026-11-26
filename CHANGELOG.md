# Changelog

Each release has a section `## [X.Y.Z]`. The Release workflow publishes that section as the release notes and stops when it is missing. Write what a user can do now, one line per change, and name breaking changes first.

## [Unreleased]

- Breaking: the default ports are now UDP 41000 (discovery), 41001 (control) and 41002 (media). The old 47700-47702 fall in the range WSL's mirrored networking reserves on Windows, where the host could not start. A host.json that still holds the old defaults moves to the new ones; ports you chose stay. Update the host and the client together: an older client listens for hosts on 47700.
- A host on a discovery port of its own also announces itself on the default one, so clients on the default list it as online.
- A lost frame reported while the host waits for the display now recovers on the next picture, instead of falling back to a keyframe one frame later.

## [0.1.0]

The first public release.

- Stream a desktop or a game from a Windows or macOS host to a Windows or macOS client, with hardware encode on NVIDIA (NVENC), AMD (AMF), Intel (oneVPL) and Apple (VideoToolbox) GPUs.
- HEVC, AV1 and H.264, with 4:4:4 chroma and 10-bit HDR10 where the GPU supports them.
- Encrypted low-latency UDP media with adaptive error correction and rate control.
- Find and pair hosts on the local network and on Tailscale; same-user Tailscale devices pair without a PIN.
- Audio, microphone, gamepads, keyboard, mouse and clipboard.
- Windows installer (no administrator rights needed) and portable zip, macOS DMGs for Apple silicon and Intel, and a Linux command-line preview.
- Anonymous usage telemetry through TelemetryDeck: install, launch and stream counts with the codec and route type, under a hashed random ID, and never names, addresses, screen content, audio, input or PINs. It is on by default; turn it off in Settings › General, or with `planck-host -no-telemetry`. See [Privacy](README.md#privacy-anonymous-usage-telemetry).
