# Planck

Open-source, low-latency game streaming: a host and a native desktop client, written in pure Go (`CGO_ENABLED=0`) with a [MyGo](https://github.com/egoist/mygo) interface.

- **Website:** [planck.gg](https://planck.gg)
- **License:** [Apache-2.0](LICENSE)

## Download

| Platform | File |
|---|---|
| Windows 10 (2004) or later, x64 | [Installer](https://planck.gg/api/download?platform=windows) (`planck-<version>-windows-x64-setup.exe`), or the portable `planck-<version>-windows-x64.zip` |
| macOS 14 or later | [Apple silicon DMG](https://planck.gg/api/download?platform=macos) (`planck-<version>-macos-arm64.dmg`), or `planck-<version>-macos-amd64.dmg` for Intel Macs |
| Linux x86_64, aarch64 | `planck-<version>-linux-<arch>.tar.gz`: a command-line preview with no desktop window or streaming yet |

Every file is on the [Releases](https://github.com/planckdotgg/planck/releases) page, with `SHA256SUMS.txt`.

The Windows installer needs no administrator rights. It installs `planck-client.exe` (the app) and `planck-host.exe` (a command-line host for headless machines). The macOS app holds both in `Planck.app/Contents/MacOS/`.

## What it does

- **Stream a desktop or a game** from a Windows or macOS host to a Windows or macOS client. Any client can also host from its **Hosting** switch.
- **Hardware encode on every major GPU:** NVIDIA NVENC, AMD AMF and Intel oneVPL (Quick Sync) on Windows, and VideoToolbox on macOS. Codecs are HEVC, AV1 and H.264, with 4:4:4 chroma and 10-bit HDR10 where the GPU supports them.
- **Low-latency transport:** encrypted UDP media with adaptive forward error correction and rate control. QUIC carries control and pairing.
- **Tailscale-aware:** finds your own devices on your tailnet, sizes packets for its MTU, and prefers a direct LAN path when both ends share one.
- **Audio, microphone, gamepads, keyboard, mouse and clipboard** go both ways where the platform allows.
- **Pairing** with a 6-digit PIN, or no PIN between two devices of the same Tailscale user.

## Privacy: anonymous usage telemetry

Planck sends a few anonymous signals to [TelemetryDeck](https://telemetrydeck.com), so its developers can see how many people use it and which codecs and kinds of route their streams take. It is on by default. Turn it off in **Settings › General › Anonymous usage telemetry**. Off takes effect at once and drops any signals still waiting to be sent. The switch also covers `planck-host` on the same machine; on a machine without the app, run `planck-host -no-telemetry`, or set `"telemetryEnabled": false` in `client.json` in Planck's config folder.

There are four signals: `app.installed` (once per install), `app.launched`, `session.started` and `session.ended`. Each holds only these fields:

| Field | What it is |
|---|---|
| `clientUser` | The SHA-256 of a random ID that Planck makes on first use and keeps on the device (`telemetry.json` in the config folder). TelemetryDeck hashes it again with a salt. It is not linked to you, your accounts or your hardware. |
| `sessionID` | A random ID for each launch |
| `os`, `arch`, `platform` | For example `windows`, `amd64`, `desktop` |
| `appVersion`, `buildChannel` | For example `0.1.0`, `release` |
| `mode` | `client` or `host`: the side of the stream |
| `codec` | Only in `session.*`: `h264`, `hevc`, `av1`, `pyrowave` or `unknown` |
| `transportKind` | Only in `session.*`: `lan`, `tailscale_direct`, `tailscale_relay` or `unknown` |

Planck never sends host or device names, IP addresses, Tailscale addresses or identities, usernames, window titles, screen content, audio, input, clipboard contents, or pairing PINs and keys. TelemetryDeck sees your IP address, as any server does, and its [privacy FAQ](https://telemetrydeck.com/docs/guides/privacy-faq/) says it never stores IP addresses.

A build from source sends nothing: only the release builds carry a TelemetryDeck App ID. A source build given one with `PLANCK_TELEMETRY_APP_ID` marks its signals as test signals. `planck-client -headless` never sends telemetry.

## Experimental drivers (not installed)

The release files contain no drivers. These are separate, optional, and experimental. Install them only to test them:

- **Planck Virtual Audio**, a virtual speaker and microphone that carries the client's microphone into games and voice chat on the host. The Windows kernel driver is in [`drivers/audio/windows`](drivers/audio/windows/README.md) and the macOS HAL plug-in in [`drivers/audio/darwin`](drivers/audio/darwin/README.md). Without it, the host uses Steam Streaming Microphone, VB-CABLE or BlackHole if one is installed.
- **SudoVDA** (Windows, a third-party virtual display driver), which lets the host stream at the client's resolution and refresh rate on a display that does not exist. Planck uses it when it is installed and falls back to a real display when it is not.

## Build from source

Planck needs Go (the version in `go.mod`) and no C toolchain.

```sh
CGO_ENABLED=0 go build ./cmd/planck-client ./cmd/planck-host
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./cmd/... ./internal/... ./pkg/...
```

`planck-client -version` and `planck-host -version` print the version. `planck-client -headless` streams without a window and prints latency figures; `-help` lists every option.

The release packages are built by these scripts, which [docs/releasing.md](docs/releasing.md) describes:

| Script | Output in `dist/` |
|---|---|
| `scripts/release-macos.sh --skip-sign` | `planck-<version>-macos-<arch>.dmg`, unsigned |
| `scripts\bundle-windows.ps1 -Installer` | the Windows zip and installer (needs [Inno Setup 6](https://jrsoftware.org/isinfo.php)) |
| `scripts/bundle-linux.sh` | the Linux tarballs |

The `cmd/spike-*` and `cmd/planck-spike2` programs are Phase 0 experiments, and `cmd/planck-trace` and `cmd/planck-vdd` are developer tools. None of them ship.

## License

Planck is licensed under the [Apache License 2.0](LICENSE). [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists the parts that come from other projects.
