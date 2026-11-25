# Planck Virtual Audio (macOS)

A Core Audio HAL plug-in (`AudioServerPlugIn`) that adds one device,
**Planck Virtual Audio**, with an output and an input wired together:
whatever plays to it is recorded from it. `planck-host` plays a client's
microphone into the output side, and games, Discord, or OBS record the input
side.

| | |
|---|---|
| Bundle | `PlanckAudio.driver`, identifier `gg.planck.audio.driver` |
| Device | "Planck Virtual Audio", UID `gg.planck.audio.device`, transport Virtual |
| Format | 48 kHz, 32-bit float, stereo, on both sides; nothing else is offered |
| Architectures | Universal (arm64, x86_64) |
| Minimum OS | macOS 11 |

It is Planck's own code (Apache-2.0), written against Apple's
`CoreAudio/AudioServerPlugIn.h` in plain C. It uses no third-party code and
needs only the macOS SDK and CMake to build, with no network access.

## How it works

`coreaudiod` loads the bundle and calls `PlanckAudio_Create`, the CFPlugIn
factory named in `Info.plist`. The driver publishes one device with two
streams, keeps its own clock from `mach_absolute_time`, and runs no threads.

- **Output** (`WriteMix`): the mixed frames go into a 16384-frame ring
  (about 341 ms), at their sample time.
- **Input** (`ReadInput`): frames are read back from the ring at the input
  sample time. A frame that was not played in the current run of output, or
  was overwritten by a later lap of the ring, reads as silence, so old
  audio never comes back after a pause.
- Starting IO from idle clears the ring and starts a new time line.

### Why the format cannot mismatch

Both streams list exactly one physical and virtual format, the device lists
exactly one nominal rate (48 kHz), and setting any other rate or format
fails with `kAudioDeviceUnsupportedFormatError`. Core Audio converts each
client's format (mono, int16, 44.1 kHz) to and from that one, on both sides.

### Other choices

- The device may be the default input but never the default output, so
  macOS does not send game audio into the microphone on its own.
- It has no volume or mute controls: the host sets the level it plays at,
  and apps control their own input level.

## Build

Needs Xcode or the Command Line Tools, and CMake 3.20 or later.

```sh
./build.sh
```

`build.sh` configures and builds into `build/` (set `PLANCK_AUDIO_BUILD_DIR`
to change it), runs the user-mode tests (`tests/driver_test.c` drives the
plug-in through its factory: properties, the locked format, and loopback
IO), and ad-hoc signs `build/PlanckAudio.driver`. It installs nothing.

An ad-hoc signature is for the Mac that built the bundle.

## Install for development

Installing restarts Core Audio, which interrupts all sound for a moment.

```sh
sudo cp -R build/PlanckAudio.driver /Library/Audio/Plug-Ins/HAL/
sudo chown -R root:wheel /Library/Audio/Plug-Ins/HAL/PlanckAudio.driver
sudo killall coreaudiod
```

"Planck Virtual Audio" then shows in Sound settings and in Audio MIDI Setup.
Planck finds it by name: Settings, Audio, "Audio when hosting" says
**Planck Virtual Audio is running.** on a host where Core Audio has loaded
it.

Remove it with:

```sh
sudo rm -rf /Library/Audio/Plug-Ins/HAL/PlanckAudio.driver
sudo killall coreaudiod
```

## Release signing and notarization

A bundle downloaded from the internet must be signed with a Developer ID
Application certificate and notarized, or Gatekeeper blocks it. A HAL
plug-in needs no entitlement from Apple.

1. Join the Apple Developer Program and create a **Developer ID
   Application** certificate in the signing Mac's keychain.
2. Store notarization credentials once, under a profile name:

   ```sh
   xcrun notarytool store-credentials planck --apple-id <apple id> --team-id <team id>
   ```

3. Build, sign with the hardened runtime and a secure timestamp, notarize,
   and staple:

   ```sh
   PLANCK_SIGN_IDENTITY="Developer ID Application: <name> (<team id>)" \
   PLANCK_NOTARY_PROFILE=planck ./build.sh
   ```

The stapled bundle is what an installer copies into
`/Library/Audio/Plug-Ins/HAL/` (with an administrator prompt) before it
restarts `coreaudiod`.
