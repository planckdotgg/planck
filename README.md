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
