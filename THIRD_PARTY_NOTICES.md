# Third-party notices

Planck is Apache-2.0 (see `LICENSE`). These parts of the repository come from
other projects and keep their own licenses. Go module dependencies are
fetched by the Go toolchain and carry their licenses with them.

| Path | Origin | License |
|---|---|---|
| `drivers/audio/windows/` | [VirtualDrivers/Virtual-Audio-Driver](https://github.com/VirtualDrivers/Virtual-Audio-Driver) at `bb34fba15faf`, by MikeTheTech | MIT |
| `drivers/audio/windows/` (files marked "Copyright (c) Microsoft Corporation") | [Microsoft Windows Driver Samples](https://github.com/microsoft/Windows-driver-samples), SysVAD simple audio sample | Microsoft Public License (MS-PL) |
| `third_party/mygo/` | [egoist/mygo](https://github.com/egoist/mygo) | MIT |
| `assets/fonts/Archivo*` | Archivo | SIL Open Font License 1.1 |
| `assets/fonts/MartianMono*` | Martian Mono | SIL Open Font License 1.1 |

The full license texts are in `drivers/audio/windows/LICENSE` (MIT and MS-PL),
`third_party/mygo/LICENSE`, and `assets/fonts/OFL-*.txt`.

`drivers/audio/darwin/` (the macOS Planck Virtual Audio plug-in) is Planck's
own code under Apache-2.0 and contains no third-party code. Planck does not
bundle BlackHole or BackgroundMusic (GPL), or VB-CABLE (no redistribution
grant).

The Planck Virtual Audio driver is a separate kernel-mode program built from
`drivers/audio/windows/`; it is not linked into the Go binaries, which stay
Apache-2.0 and pure Go (`CGO_ENABLED=0`). MS-PL requires that source
distributions of the Microsoft-derived files keep the MS-PL text and their
notices, and that binary distributions be under a license that complies with
MS-PL.
