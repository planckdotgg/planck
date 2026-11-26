# Build the Windows release files in dist\:
#   planck-<version>-windows-x64.zip         the portable app folder
#   planck-<version>-windows-x64-setup.exe   with -Installer (Inno Setup 6)
# each with its .sha256.
#
#   powershell -ExecutionPolicy Bypass -File scripts\bundle-windows.ps1 [-Installer]
#
# Signing: pass -SignCert <file.pfx> and -SignPassword, or set
# PLANCK_SIGN_CERT and PLANCK_SIGN_PASSWORD, and signtool signs both exes and
# the installer. Without a certificate nothing is signed, and SmartScreen
# warns on download.
param(
    [switch]$Installer,
    [string]$SignCert = $env:PLANCK_SIGN_CERT,
    [string]$SignPassword = $env:PLANCK_SIGN_PASSWORD
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Invoke-Checked([string]$what, [scriptblock]$block) {
    Write-Host "==> $what"
    & $block
    if ($LASTEXITCODE -ne 0) { throw "$what failed (exit $LASTEXITCODE)" }
}

function Write-Sha256([string]$path) {
    $hash = (Get-FileHash -Algorithm SHA256 $path).Hash.ToLower()
    Set-Content -Encoding ascii -NoNewline -Path "$path.sha256" -Value "$hash  $(Split-Path -Leaf $path)`n"
}

$version = (Select-String -Path internal\version\version.go -Pattern '^var Version = "(.+)"$' | Select-Object -First 1).Matches[0].Groups[1].Value
if (-not $version) { throw 'no version in internal\version\version.go' }
$commit = (git rev-parse --short=12 HEAD 2>$null)
if (-not $commit) { $commit = 'unknown' }
$buildDate = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$pkg = 'github.com/planckdotgg/planck/internal/version'
$ldflags = "-s -w -X $pkg.Commit=$commit -X $pkg.BuildDate=$buildDate"
# A release (PLANCK_OFFICIAL_BUILD=1, set by the Release workflow) sends its
# telemetry as a release, not as test signals. PLANCK_TELEMETRY_APP_ID is the
# TelemetryDeck App ID of the official app (internal\telemetry); without it
# the build sends no telemetry.
if ($env:PLANCK_OFFICIAL_BUILD -eq '1') { $ldflags += " -X $pkg.OfficialBuild=1" }
if ($env:PLANCK_TELEMETRY_APP_ID) { $ldflags += " -X github.com/planckdotgg/planck/internal/telemetry.DefaultAppID=$($env:PLANCK_TELEMETRY_APP_ID)" }

$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

$dist = Join-Path $root 'dist'
$app = Join-Path $dist 'windows-x64\Planck'
if (Test-Path $app) { Remove-Item -Recurse -Force $app }
New-Item -ItemType Directory -Force $app | Out-Null

# The .syso files are committed; generating them again here keeps a
# release correct even if a version bump forgot to.
Invoke-Checked 'icon and version resources' { go run .\scripts\winres generate }
$client = Join-Path $app 'planck-client.exe'
$hostExe = Join-Path $app 'planck-host.exe'
Invoke-Checked "planck-client $version" { go build -trimpath -tags mygo_noinspector "-ldflags=$ldflags -H=windowsgui" -o $client .\cmd\planck-client }
Invoke-Checked "planck-host $version" { go build -trimpath "-ldflags=$ldflags" -o $hostExe .\cmd\planck-host }
Invoke-Checked 'check planck-client resources' { go run .\scripts\winres check -version $version $client }
Invoke-Checked 'check planck-host resources' { go run .\scripts\winres check -version $version $hostExe }
Copy-Item LICENSE (Join-Path $app 'LICENSE.txt')
Copy-Item THIRD_PARTY_NOTICES.md $app

$signtool = $null
if ($SignCert) {
    if (-not (Test-Path $SignCert)) { throw "the signing certificate $SignCert does not exist" }
    $signtool = (Get-Command signtool.exe -ErrorAction SilentlyContinue).Source
    if (-not $signtool) {
        $signtool = (Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -Last 1).FullName
    }
    if (-not $signtool) { throw 'a signing certificate was given, but signtool.exe was not found' }
} else {
    Write-Warning 'No signing certificate: the exes and the installer are not signed, and SmartScreen will warn.'
}
function Sign-File([string]$path) {
    if (-not $signtool) { return }
    Invoke-Checked "sign $(Split-Path -Leaf $path)" {
        & $signtool sign /f $SignCert /p $SignPassword /fd sha256 /tr http://timestamp.digicert.com /td sha256 $path
    }
}
Sign-File $client
Sign-File $hostExe

$name = "planck-$version-windows-x64"
$zip = Join-Path $dist "$name.zip"
$stage = Join-Path $dist "windows-x64\$name"
if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
Copy-Item -Recurse $app $stage
if (Test-Path $zip) { Remove-Item -Force $zip }
Write-Host "==> $zip"
Compress-Archive -Path $stage -DestinationPath $zip
Remove-Item -Recurse -Force $stage
Write-Sha256 $zip

if ($Installer) {
    $iscc = (Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source
    if (-not $iscc) {
        $iscc = @("${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
    }
    if (-not $iscc) { throw 'Inno Setup 6 (ISCC.exe) was not found. Install it, or leave out -Installer.' }
    Invoke-Checked 'installer (Inno Setup)' {
        & $iscc /Qp "/DAppVersion=$version" "/DSourceDir=$app" "/DOutputDir=$dist" packaging\windows\Planck.iss
    }
    $setup = Join-Path $dist "$name-setup.exe"
    Sign-File $setup
    Write-Sha256 $setup
    Write-Host "==> installer: $setup"
}
Get-ChildItem $dist -File | Format-Table Name, Length
