# Planck Virtual Audio (Windows)

A kernel-mode audio driver that gives Windows one speaker and one microphone,
wired together: whatever plays to **Speakers (Planck Virtual Audio)** comes out
of **Microphone (Planck Virtual Audio)**. `planck-host` plays the client's
microphone into the speaker side, and games, Discord, or OBS record the
microphone side.

| | |
|---|---|
| Driver | `PlanckVAD.sys` (KMDF 1.15, PortCls WaveRT miniport) |
| INF | `PlanckVAD.inf`, class `MEDIA`, provider and manufacturer `Planck` |
| Hardware ID | `ROOT\PlanckVirtualAudio` (root-enumerated, one instance) |
| Service | `PlanckVAD` |
| Formats | 48 kHz, 16-bit PCM, mono or stereo (stereo is the default) |
| Minimum OS | Windows 10 2004 (build 19041), x64 |

It is derived from [VirtualDrivers/Virtual-Audio-Driver](https://github.com/VirtualDrivers/Virtual-Audio-Driver)
(MIT), itself built on Microsoft's SysVAD sample (MS-PL). See `LICENSE`.

## How it works

The speaker and microphone are two WaveRT pins of one driver. Each pin's
position advances on the same `KeQueryPerformanceCounter` clock, from the
stream's timer DPC or a position query, so the two can never drift apart.

- **Render** (`CMiniportWaveRTStream::ReadBytes`): as the speaker's position
  advances, the frames Windows rendered are copied into a ring
  (`src/Inc/loopback.h`), converted to stereo (mono is duplicated to both
  channels).
- **Capture** (`WriteBytes`): as the microphone's position advances, its
  buffer is filled from the ring, converted to the capture format (stereo to
  mono is `(L + R) / 2`). With no speaker stream running, it is silence.

The ring is 8192 stereo frames (about 170 ms) of static non-paged memory with
one spin lock. Nothing is allocated, so there is nothing to leak. Ring calls
run at `DISPATCH_LEVEL` inside the stream's position lock; the ring lock is
always taken second. Only the one speaker stream in `RUN` writes. A reader
that would overtake the writer gets silence and keeps its place, which builds
a cushion against timer jitter. A reader more than 80 ms behind skips ahead
so the delay stays bounded. A microphone that starts reading begins 20 ms
behind the writer, and never reads audio from before the current speaker
stream started. Copy-protected content (`IDrmAudioStream::SetContentId` with
`CopyProtect`) is never copied into the ring.

### Why the rates can never mismatch

Steam Streaming Microphone copies bytes from its speaker to its microphone
without converting. When the two sides are set to different formats (44.1
against 48 kHz, or mono against stereo), apps hear silence or noise. Here:

- every pin offers exactly two formats, 48 kHz 16-bit stereo and mono, in its
  data range, device formats, and `DataRangeIntersection`, so no 44.1 kHz
  format can be chosen on either side;
- the stream rejects any other format at creation (`STATUS_NO_MATCH`);
- the ring converts between mono and stereo, so a 1-channel and a 2-channel
  app, on either side, pair correctly.

### Why paired endpoints and not a private IOCTL

A direct PCM IOCTL would skip the Windows audio engine on the speaker side.
It would cost a second kernel entry point (a custom device interface on a
PortCls driver, which owns the dispatch table, plus its own security
descriptor and buffer validation) and a new attack surface open to every
process that can open the device. It would also need its own clock and
backpressure. The paired endpoints need none of that: `planck-host` already
renders into a virtual speaker through WASAPI (`pkg/audio`), the audio engine
converts its mono float stream to the locked format, and the kernel's only
new job is a bounded copy between two buffers it already owns. The IOCTL can
be added later if measurements show the audio engine's extra buffer matters.

### Changes from the upstream sample

- Removed the tone generator and the render-to-file writer (and their
  registry settings and work items); the ring replaces both.
- Locked all pins to 48 kHz 16-bit mono or stereo; the microphone node type
  is `KSNODETYPE_MICROPHONE`, so the endpoint is named "Microphone".
- Checked the size of client data formats (`FormatSize`, `cbSize`) before
  reading them in `IsFormatSupported` and stream creation.
- Checked `MapAllocatedPages` for failure (the MDL is freed on that path).
- Dropped the copy of the registry path, which assumed a NUL-terminated
  `UNICODE_STRING` and was never used.
- Dropped the INF's DRM signature attributes: a loopback device must not claim
  to protect content.

## Build

Needs Visual Studio 2022 Build Tools with the C++ x64 tools
(`Microsoft.VisualStudio.Component.VC.Tools.x86.x64`) and internet access for
the first run. No WDK installer, Visual Studio WDK component, or elevation is
needed.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File build.ps1 -TestSign
```

`build.ps1`:

1. downloads `nuget.exe` (pinned version and SHA-256) and restores the WDK and
   SDK NuGet packages pinned in `packages.config` into `packages\`;
2. builds and runs the ring's user-mode tests (`tests\loopback_test.cpp`);
3. compiles and links `PlanckVAD.sys` with MSVC (`/kernel`, `/W4 /WX`, CFG,
   `/INTEGRITYCHECK`) using the WDK's kernel-mode flags;
4. runs ApiValidator against the Universal DDI list;
5. stamps `PlanckVAD.inf` from `src\Main\PlanckVAD.inx` and checks it with
   InfVerif `/w` (Windows driver) and `/u` (Universal driver);
6. makes `PlanckVAD.cat` with Inf2Cat for Windows 10 2004 through Windows 11;
7. with `-TestSign`, signs the `.sys` and `.cat` with a throwaway certificate
   and verifies them (see below).

Output: `build\x64\Release\package\` (`PlanckVAD.sys`, `.inf`, `.cat`), and
`build\testsign\PlanckVAD-test.cer`. Options: `-Configuration Debug`,
`-Version 1.2.3.4` (stamps the INF's `DriverVer` and the file version),
`-Clean`.

The build calls `cl` and `link` directly rather than MSBuild. The WDK NuGet
packages contain the kit but not the `WindowsKernelModeDriver10.0` platform
toolset; that comes from Visual Studio's "Windows Driver Kit" component, which
only the Visual Studio Installer can add, elevated. The flags in `build.ps1`
are taken from the WDK's `WindowsDriver.*.props`.

### Test signing

`-TestSign` creates a self-signed code-signing certificate, exports its public
part to `build\testsign\PlanckVAD-test.cer`, and deletes the private key as
soon as the files are signed. It touches no trust store. Because nothing
trusts that certificate, `signtool verify /pa` ends in "terminated in a root
certificate which is not trusted". `build.ps1` accepts that error and only
that one: signtool has checked each signature and the catalog's hashes of the
`.sys` and `.inf` before it reports it. It then checks the signer is the
throwaway certificate.

## Install on a test machine

Use a Hyper-V VM or a spare PC, never a machine you rely on: test signing
lowers its kernel code integrity, and Secure Boot must be off for it.

```powershell
# elevated, once, on the test machine
bcdedit /set testsigning on
# reboot, then copy build\x64\Release\package and build\testsign to it, and:
.\install.ps1 -TrustTestCertificate
```

`install.ps1` adds the package to the driver store and creates
`ROOT\PlanckVirtualAudio` (or updates an existing device). It refuses a
test-signed package when test signing is off. Remove everything with:

```powershell
.\uninstall.ps1 -RemoveTestCertificate
bcdedit /set testsigning off
```

Windows may make a new speaker the default output device. If it picks
**Speakers (Planck Virtual Audio)**, game audio goes into the microphone:
set your real speakers back as default in Sound settings.

## Release signing

Windows 10 1607 and later load only kernel drivers that Microsoft has signed.
Two routes:

1. **Attestation signing** (Windows 10 and 11 desktop, no HLK run):
   - get an EV code-signing certificate and register a company account in the
     Microsoft Partner Center hardware dashboard;
   - build with `build.ps1 -Version <x.y.z.w>` (no `-TestSign`);
   - package the `.sys`, `.inf` and `.pdb` into a CAB and sign the CAB with
     the EV certificate: `makecab /f planckvad.ddf`, then
     `signtool sign /fd SHA256 /tr <timestamp URL> /td SHA256 /n "<EV subject>" PlanckVAD.cab`;
   - submit the CAB as a driver for attestation; Microsoft returns a package
     with its signature on the `.sys` and a new `.cat`.
2. **WHCP** (Windows Hardware Compatibility Program): run the HLK audio tests
   on Windows 11 (and Windows Server, if needed), submit the HLK package in
   Partner Center, and get the same Microsoft signature plus Windows Update
   distribution.

The attested package installs on stock Windows with Secure Boot on, using
`install.ps1` without `-TrustTestCertificate`.
