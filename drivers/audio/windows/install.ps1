<#
.SYNOPSIS
Installs Planck Virtual Audio from a built driver package. Run elevated.

.DESCRIPTION
Adds the package to the driver store and creates the root-enumerated device
ROOT\PlanckVirtualAudio (as devcon install would), or updates the driver of
an existing one. Windows only loads the driver if its signature is trusted:
a retail (attestation or WHCP) signature, or a test signature on a machine
with test signing on.

For a test-signed build, run it only on a test machine (a Hyper-V VM or a
spare PC) where you have already run, elevated:

    bcdedit /set testsigning on      # then reboot

and pass -TrustTestCertificate to add the build's ephemeral certificate to
that machine's Trusted Root and Trusted Publishers stores. Never do either
on a machine you rely on: test signing lowers its kernel code integrity.

.PARAMETER PackageDir
The folder with PlanckVAD.inf, .sys and .cat. Default: build\x64\Release\package.

.PARAMETER TestCertificate
The test certificate -TrustTestCertificate adds. Default: build\testsign\PlanckVAD-test.cer.

.EXAMPLE
.\install.ps1 -TrustTestCertificate
#>
[CmdletBinding()]
param(
    [string]$PackageDir,
    [switch]$TrustTestCertificate,
    [string]$TestCertificate
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3

# Windows PowerShell 5.1 leaves $PSScriptRoot empty in parameter defaults.
if (-not $PackageDir) { $PackageDir = Join-Path $PSScriptRoot 'build\x64\Release\package' }
if (-not $TestCertificate) { $TestCertificate = Join-Path $PSScriptRoot 'build\testsign\PlanckVAD-test.cer' }

$HardwareId = 'ROOT\PlanckVirtualAudio'

$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'install.ps1 must run elevated (Run as administrator).'
}

$inf = (Resolve-Path (Join-Path $PackageDir 'PlanckVAD.inf')).Path
foreach ($f in 'PlanckVAD.sys', 'PlanckVAD.cat') {
    if (-not (Test-Path (Join-Path $PackageDir $f))) { throw "$f is missing from $PackageDir; run build.ps1 first" }
}

$signature = Get-AuthenticodeSignature (Join-Path $PackageDir 'PlanckVAD.cat')
$testSigned = $signature.SignerCertificate -and $signature.SignerCertificate.Subject -like '*Planck VAD Test Signing*'
if ($testSigned) {
    $testsigning = (bcdedit /enum '{current}' | Select-String -Pattern '^testsigning\s+Yes' -Quiet)
    if (-not $testsigning) {
        throw 'This package is test-signed and test signing is off, so Windows would refuse to load it. Install it on a test machine with test signing on (see README.md).'
    }
    if ($TrustTestCertificate) {
        $trusted = New-Object Security.Cryptography.X509Certificates.X509Certificate2 (Resolve-Path $TestCertificate).Path
        if ($trusted.Thumbprint -ne $signature.SignerCertificate.Thumbprint) {
            throw "$TestCertificate ($($trusted.Thumbprint)) is not the certificate that signed this package ($($signature.SignerCertificate.Thumbprint)); refusing to trust it."
        }
        Import-Certificate -FilePath $TestCertificate -CertStoreLocation Cert:\LocalMachine\Root | Out-Null
        Import-Certificate -FilePath $TestCertificate -CertStoreLocation Cert:\LocalMachine\TrustedPublisher | Out-Null
        Write-Host "Trusted $TestCertificate on this machine."
    }
}

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class PlanckDeviceInstall
{
    const int DIF_REGISTERDEVICE = 0x19;
    const int DICD_GENERATE_ID = 0x1;
    const int SPDRP_HARDWAREID = 0x1;
    const int INSTALLFLAG_FORCE = 0x1;

    [StructLayout(LayoutKind.Sequential)]
    struct SP_DEVINFO_DATA
    {
        public int cbSize;
        public Guid ClassGuid;
        public int DevInst;
        public IntPtr Reserved;
    }

    [DllImport("setupapi.dll", SetLastError = true)]
    static extern IntPtr SetupDiCreateDeviceInfoList(ref Guid classGuid, IntPtr hwndParent);

    [DllImport("setupapi.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    static extern bool SetupDiCreateDeviceInfoW(IntPtr set, string deviceName, ref Guid classGuid, string description, IntPtr hwndParent, int flags, ref SP_DEVINFO_DATA data);

    [DllImport("setupapi.dll", SetLastError = true)]
    static extern bool SetupDiSetDeviceRegistryPropertyW(IntPtr set, ref SP_DEVINFO_DATA data, int property, byte[] buffer, int size);

    [DllImport("setupapi.dll", SetLastError = true)]
    static extern bool SetupDiCallClassInstaller(int function, IntPtr set, ref SP_DEVINFO_DATA data);

    [DllImport("setupapi.dll", SetLastError = true)]
    static extern bool SetupDiRemoveDevice(IntPtr set, ref SP_DEVINFO_DATA data);

    [DllImport("setupapi.dll", SetLastError = true)]
    static extern bool SetupDiDestroyDeviceInfoList(IntPtr set);

    [DllImport("newdev.dll", SetLastError = true, CharSet = CharSet.Unicode)]
    static extern bool UpdateDriverForPlugAndPlayDevicesW(IntPtr hwndParent, string hardwareId, string infPath, int flags, out bool rebootRequired);

    // Creates a root-enumerated device with hardwareId when create is set,
    // then installs infPath on every device with that ID. Returns whether
    // Windows asks for a reboot.
    public static bool Install(string hardwareId, string infPath, bool create)
    {
        Guid media = new Guid("4d36e96c-e325-11ce-bfc1-08002be10318");
        if (create)
        {
            IntPtr set = SetupDiCreateDeviceInfoList(ref media, IntPtr.Zero);
            if (set == new IntPtr(-1)) throw new System.ComponentModel.Win32Exception();
            try
            {
                SP_DEVINFO_DATA data = new SP_DEVINFO_DATA();
                data.cbSize = Marshal.SizeOf(typeof(SP_DEVINFO_DATA));
                if (!SetupDiCreateDeviceInfoW(set, "MEDIA", ref media, null, IntPtr.Zero, DICD_GENERATE_ID, ref data))
                    throw new System.ComponentModel.Win32Exception();
                byte[] ids = System.Text.Encoding.Unicode.GetBytes(hardwareId + "\0\0");
                if (!SetupDiSetDeviceRegistryPropertyW(set, ref data, SPDRP_HARDWAREID, ids, ids.Length))
                    throw new System.ComponentModel.Win32Exception();
                if (!SetupDiCallClassInstaller(DIF_REGISTERDEVICE, set, ref data))
                    throw new System.ComponentModel.Win32Exception();
                bool reboot;
                if (!UpdateDriverForPlugAndPlayDevicesW(IntPtr.Zero, hardwareId, infPath, INSTALLFLAG_FORCE, out reboot))
                {
                    int err = Marshal.GetLastWin32Error();
                    SetupDiRemoveDevice(set, ref data);
                    throw new System.ComponentModel.Win32Exception(err);
                }
                return reboot;
            }
            finally
            {
                SetupDiDestroyDeviceInfoList(set);
            }
        }
        bool rebootExisting;
        if (!UpdateDriverForPlugAndPlayDevicesW(IntPtr.Zero, hardwareId, infPath, INSTALLFLAG_FORCE, out rebootExisting))
            throw new System.ComponentModel.Win32Exception();
        return rebootExisting;
    }
}
'@

$existing = @(Get-PnpDevice -ErrorAction SilentlyContinue | Where-Object { $_.HardwareID -contains $HardwareId })
Write-Host "Adding $inf to the driver store"
pnputil.exe /add-driver $inf | Out-Host
if ($LASTEXITCODE -ne 0) { throw "pnputil /add-driver failed with exit code $LASTEXITCODE" }

$reboot = [PlanckDeviceInstall]::Install($HardwareId, $inf, $existing.Count -eq 0)
if ($existing.Count -eq 0) { Write-Host "Created $HardwareId and installed Planck Virtual Audio." }
else { Write-Host "Updated the driver of $($existing.Count) existing Planck Virtual Audio device(s)." }
if ($reboot) { Write-Warning 'Windows asks for a reboot to finish.' }

Get-PnpDevice | Where-Object { $_.HardwareID -contains $HardwareId } | Format-Table -AutoSize Status, FriendlyName, InstanceId | Out-Host
Write-Host 'Endpoints: "Speakers (Planck Virtual Audio)" and "Microphone (Planck Virtual Audio)". Check that Windows did not make the speaker your default output device.'
