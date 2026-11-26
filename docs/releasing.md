# How to release Planck

A release is a tag `vX.Y.Z` on `main`. Pushing the tag runs the Release workflow (`.github/workflows/release.yml`), which builds every file, checks the set, writes `SHA256SUMS.txt`, and publishes one GitHub release. The release starts as a draft, gets all its files, and only then becomes public, so `planck.gg/api/download` (which reads `releases/latest`) never serves a release with a missing file.

## Release files

| File | Built by | Job |
|---|---|---|
| `planck-<v>-macos-arm64.dmg` | `scripts/release-macos.sh --arch arm64` | `macos` |
| `planck-<v>-macos-amd64.dmg` | `scripts/release-macos.sh --arch amd64` | `macos` |
| `planck-<v>-windows-x64-setup.exe` | `scripts\bundle-windows.ps1 -Installer` (Inno Setup, `packaging/windows/Planck.iss`) | `windows` |
| `planck-<v>-windows-x64.zip` | the same script: the portable app folder | `windows` |
| `planck-<v>-linux-x86_64.tar.gz`, `planck-<v>-linux-aarch64.tar.gz` | `scripts/bundle-linux.sh` | `linux` |
| `SHA256SUMS.txt` | the `publish` job | `publish` |

The release has exactly one `.exe`. The website takes any `.exe` as the Windows download, so a second one (a portable exe, a driver installer) would be served in place of the installer. The `publish` job fails on any file it does not expect.

No release file contains a driver. The virtual audio drivers (`drivers/audio/windows`, `drivers/audio/darwin`) and SudoVDA stay separate and experimental. The spike programs (`cmd/spike-*`, `cmd/planck-spike2`) and developer tools (`cmd/planck-trace`, `cmd/planck-vdd`) are never built for a release.

## Secrets (one time)

Signing is optional: without the secrets, the workflow builds and publishes unsigned files and logs a warning. Unsigned DMGs are blocked by Gatekeeper (the user must right-click › Open or clear the quarantine attribute), and SmartScreen warns about the unsigned installer. Set the secrets before the first public release.

Set each secret on `planckdotgg/planck` (Settings › Secrets and variables › Actions, or `gh secret set NAME --repo planckdotgg/planck`). Keep each source in 1Password and set it from there with `op read`, checking that `op read` printed a value first: `gh secret set` also accepts empty input.

### macOS: all five or none

| Secret | Contents | Format |
|---|---|---|
| `MACOS_CERTIFICATE_P12` | The Developer ID Application certificate with its private key | base64 of the `.p12` file, one line |
| `MACOS_CERTIFICATE_PASSWORD` | The password of that `.p12` | text |
| `APPLE_API_KEY_P8` | An App Store Connect API key, for notarization | the text of `AuthKey_<key id>.p8`, with its `-----BEGIN PRIVATE KEY-----` lines |
| `APPLE_API_KEY_ID` | The key ID of that API key | 10 characters, such as `ABC123DEFG` |
| `APPLE_API_ISSUER_ID` | The issuer ID of the App Store Connect team | a UUID |

The job refuses to run when only some of the five are set.

1. **Export the certificate.** In Keychain Access, under **login › My Certificates**, select only the `Developer ID Application: …` certificate that Planck ships under, choose **File › Export Items**, keep **Personal Information Exchange (.p12)**, save it outside the repository, and set a password.
2. **Create the API key.** At <https://appstoreconnect.apple.com/access/integrations/api>, on **Team Keys**, add a key with the **Developer** role. Download the `.p8` (Apple allows one download), and copy its **Key ID** and the **Issuer ID** at the top of the page.
3. **Store the secrets.**

   ```sh
   base64 -i developer-id.p12 | tr -d '\n' | gh secret set MACOS_CERTIFICATE_P12 --repo planckdotgg/planck
   gh secret set MACOS_CERTIFICATE_PASSWORD --repo planckdotgg/planck
   gh secret set APPLE_API_KEY_P8 --repo planckdotgg/planck < AuthKey_ABC123DEFG.p8
   gh secret set APPLE_API_KEY_ID --repo planckdotgg/planck --body ABC123DEFG
   gh secret set APPLE_API_ISSUER_ID --repo planckdotgg/planck --body <issuer id>
   rm developer-id.p12
   ```

   From 1Password instead, for example: `op read "op://Personal/<item>/AuthKey.p8" | gh secret set APPLE_API_KEY_P8 --repo planckdotgg/planck`.

The workflow signs with the one `Developer ID Application` identity in the `.p12`. If the file holds several, set the repository **variable** (not secret) `MACOS_SIGN_IDENTITY` to the full name, such as `Developer ID Application: Example LLC (TEAMID1234)`.

### Windows: optional

| Secret | Contents | Format |
|---|---|---|
| `WINDOWS_CERTIFICATE_PFX` | A code-signing certificate with its private key | base64 of the `.pfx` file, one line |
| `WINDOWS_CERTIFICATE_PASSWORD` | The password of that `.pfx` | text |

```sh
base64 -i codesign.pfx | tr -d '\n' | gh secret set WINDOWS_CERTIFICATE_PFX --repo planckdotgg/planck
gh secret set WINDOWS_CERTIFICATE_PASSWORD --repo planckdotgg/planck
```

With both set, `signtool` signs `planck-client.exe`, `planck-host.exe` and the installer, timestamped by DigiCert.

### Telemetry: optional

| Secret | Contents | Format |
|---|---|---|
| `PLANCK_TELEMETRY_APP_ID` | The TelemetryDeck App ID of the official app | the App ID from the TelemetryDeck dashboard, a UUID |

```sh
gh secret set PLANCK_TELEMETRY_APP_ID --repo planckdotgg/planck --body <app id>
```

It is a secret, not a repository variable. Each build job passes it to its bundle script as `PLANCK_TELEMETRY_APP_ID`, which sets `internal/telemetry.DefaultAppID` with `-ldflags -X`. Unset, the release files send no telemetry. A run that publishes also sets `PLANCK_OFFICIAL_BUILD=1` (`internal/version.OfficialBuild`), so its files send release signals. A dry run's files send test signals, which TelemetryDeck shows only in test mode. The README's Privacy section says what the signals hold.

## Dry run

A dry run builds every file on clean runners, signs nothing, sends nothing to Apple, and publishes nothing. The files and `SHA256SUMS.txt` stay on the run as artifacts (`release-macos`, `release-windows`, `release-linux`, `sha256sums`).

```sh
gh workflow run release.yml --repo planckdotgg/planck --ref <branch>
gh run watch --repo planckdotgg/planck
```

In the browser: **Actions › Release › Run workflow**, with **dry_run** checked. The optional **version** input stops the run unless `internal/version/version.go` has that version.

## Make a release

1. **Set the version.** `Version` in `internal/version/version.go` is the one source: the scripts read it with `scripts/version.sh`, and the workflow stops unless the tag is `v<Version>`. Then write the icon and version resources of the Windows exes again, which a test checks:

   ```sh
   go run ./scripts/winres generate
   ```

2. **Write the notes.** Move the lines under `## [Unreleased]` in `CHANGELOG.md` into a new `## [X.Y.Z]` section. The workflow publishes that section as the release notes and stops when it is missing.
3. **Run the checks**, and commit the three changed files (`version.go`, the two `rsrc_windows_amd64.syso`) and `CHANGELOG.md` to `main`.

   ```sh
   CGO_ENABLED=0 go vet ./...
   CGO_ENABLED=0 go test ./cmd/... ./internal/... ./pkg/...
   ```

4. **Dry-run** from `main` (above) if the packaging changed since the last release.
5. **Tag and push.**

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

   Or dispatch the workflow with **dry_run** unchecked: it creates the tag `v<Version>` at the head of the branch you run it on.

6. **Check the release.** It must be the latest release and hold the six files of the table above plus `SHA256SUMS.txt`. `https://planck.gg/api/download?platform=windows` and `?platform=macos` must redirect to the installer and the arm64 DMG (the website keeps the release list for two minutes).

A version with a suffix (`0.2.0-rc.1`) is published as a prerelease and never becomes the latest release.

## When something fails

- **A build job fails:** nothing was published (the `publish` job needs every build). Fix the cause on `main`; if the tag already points to the old commit, delete the tag (`git push origin :refs/tags/vX.Y.Z`, then `git tag -d vX.Y.Z`), and tag again.
- **The `publish` job fails after creating the draft:** the draft is not public. Run the failed job again; it deletes the draft and creates it again.
- **A published release is wrong:** the workflow never changes a published release. Make a new version. To stop the website serving it at once, mark it as a prerelease in the GitHub UI so the previous release becomes the latest again.

## Build the release files locally

```sh
scripts/release-macos.sh --skip-sign                     # unsigned DMG for this Mac's arch
scripts/release-macos.sh --arch amd64 --skip-sign        # the Intel DMG
PLANCK_SIGN_IDENTITY="Developer ID Application: …" PLANCK_NOTARY_PROFILE=<profile> scripts/release-macos.sh
scripts/bundle-linux.sh                                  # both Linux tarballs, from any OS
```

On Windows (PowerShell):

```powershell
powershell -ExecutionPolicy Bypass -File scripts\bundle-windows.ps1 -Installer
```

`release-macos.sh` uses `create-dmg` when it is installed (`brew install create-dmg`) and plain `hdiutil` otherwise. With a Developer ID identity it notarizes the app and the DMG unless `--skip-notarize` is given, with an App Store Connect key (`APPLE_API_KEY_PATH`, `APPLE_API_KEY_ID`, `APPLE_API_ISSUER_ID`) or a `notarytool` keychain profile (`PLANCK_NOTARY_PROFILE`). Everything goes to `dist/`, which git ignores.

For an install of your own checkout rather than a release, see [Local development install](../README.md#local-development-install).
