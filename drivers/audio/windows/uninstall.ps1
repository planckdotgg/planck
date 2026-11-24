<#
.SYNOPSIS
Removes Planck Virtual Audio: its devices and its driver packages. Run elevated.

.DESCRIPTION
Removes every device with hardware ID ROOT\PlanckVirtualAudio, then deletes
every PlanckVAD.inf package from the driver store. -RemoveTestCertificate
also takes the build's test certificate out of the machine's Trusted Root
and Trusted Publishers stores, undoing install.ps1 -TrustTestCertificate.

.EXAMPLE
.\uninstall.ps1 -RemoveTestCertificate
#>
[CmdletBinding()]
param(
    [switch]$RemoveTestCertificate
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3

$HardwareId = 'ROOT\PlanckVirtualAudio'

$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'uninstall.ps1 must run elevated (Run as administrator).'
}

$devices = @(Get-PnpDevice -ErrorAction SilentlyContinue | Where-Object { $_.HardwareID -contains $HardwareId })
foreach ($d in $devices) {
    Write-Host "Removing device $($d.InstanceId)"
    pnputil.exe /remove-device $d.InstanceId | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "pnputil /remove-device failed with exit code $LASTEXITCODE" }
}

$packages = @(Get-WindowsDriver -Online -All | Where-Object { $_.OriginalFileName -like '*\planckvad.inf' })
foreach ($p in $packages) {
    Write-Host "Deleting driver package $($p.Driver) ($($p.Version))"
    pnputil.exe /delete-driver $p.Driver /uninstall /force | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "pnputil /delete-driver failed with exit code $LASTEXITCODE" }
}

if ($RemoveTestCertificate) {
    foreach ($store in 'Cert:\LocalMachine\Root', 'Cert:\LocalMachine\TrustedPublisher') {
        Get-ChildItem $store | Where-Object { $_.Subject -like '*Planck VAD Test Signing*' } | ForEach-Object {
            Write-Host "Removing $($_.Thumbprint) from $store"
            Remove-Item $_.PSPath
        }
    }
}

Write-Host "Removed $($devices.Count) device(s) and $($packages.Count) driver package(s)."
