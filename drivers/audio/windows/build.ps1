<#
.SYNOPSIS
Builds PlanckVAD.sys and its driver package (INF, catalog) from source.

.DESCRIPTION
Restores the pinned WDK and SDK NuGet packages, compiles and links the driver
with MSVC using the WDK's kernel-mode flags, stamps and verifies the INF, and
makes the catalog. It needs only Visual Studio 2022 Build Tools with the C++
x64 tools: no WDK installer, no Visual Studio WDK component, no elevation.

-TestSign makes a throwaway code-signing certificate in build\testsign,
signs the .sys and .cat with it, and verifies both signatures. It never
installs anything or changes the machine's trust settings.

.EXAMPLE
powershell -NoProfile -ExecutionPolicy Bypass -File build.ps1 -TestSign
#>
[CmdletBinding()]
param(
    [ValidateSet('Release', 'Debug')]
    [string]$Configuration = 'Release',
    [ValidatePattern('^\d+\.\d+\.\d+\.\d+$')]
    [string]$Version = '1.0.0.0',
    [switch]$TestSign,
    [switch]$Clean
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3

$Root = $PSScriptRoot
$WdkVersion = '10.0.26100.6584'
$KitVersion = '10.0.26100.0'
$KmdfVersion = '1.15'
$NuGetVersion = '6.14.0'
$NuGetSha256 = '92DBED160DDEE0F64B901E907439E021211B428E57C089ECC12FC38DCC4BD9A5'
# NTDDI_WIN10_VB (Windows 10 2004, build 19041): the first release with
# ExAllocatePool2. The INF's decoration names the same floor.
$NtddiVersion = '0x0A000008'
$CatalogOs = '10_VB_X64,10_CO_X64,10_NI_X64,10_GE_X64'

$BuildDir = Join-Path $Root 'build'
$ObjDir = Join-Path $BuildDir "obj\x64\$Configuration"
$OutDir = Join-Path $BuildDir "x64\$Configuration"
$PackageDir = Join-Path $OutDir 'package'
$Packages = Join-Path $Root 'packages'
$ToolsDir = Join-Path $Root '.tools'

function Invoke-Tool {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments | Out-Host
    if ($LASTEXITCODE -ne 0) {
        throw "$(Split-Path -Leaf $Exe) failed with exit code $LASTEXITCODE"
    }
}

function Get-NuGet {
    $nuget = Join-Path $ToolsDir 'nuget.exe'
    if (-not (Test-Path $nuget)) {
        New-Item -ItemType Directory -Force $ToolsDir | Out-Null
        Invoke-WebRequest -UseBasicParsing -Uri "https://dist.nuget.org/win-x86-commandline/v$NuGetVersion/nuget.exe" -OutFile $nuget
    }
    $hash = (Get-FileHash -Algorithm SHA256 $nuget).Hash
    if ($hash -ne $NuGetSha256) {
        Remove-Item $nuget
        throw "nuget.exe SHA-256 is $hash, expected $NuGetSha256"
    }
    $nuget
}

function Restore-Kits {
    $nuget = Get-NuGet
    Invoke-Tool $nuget @('restore', (Join-Path $Root 'packages.config'), '-PackagesDirectory', $Packages, '-NonInteractive', '-Verbosity', 'quiet')
}

function Find-Msvc {
    $vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
    if (-not (Test-Path $vswhere)) { throw 'vswhere.exe not found: install Visual Studio 2022 Build Tools with the C++ x64 tools' }
    $vs = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
    if (-not $vs) { throw 'No Visual Studio install has the C++ x64 tools (Microsoft.VisualStudio.Component.VC.Tools.x86.x64)' }
    $ver = (Get-Content (Join-Path $vs 'VC\Auxiliary\Build\Microsoft.VCToolsVersion.default.txt') -TotalCount 1).Trim()
    $msvc = Join-Path $vs "VC\Tools\MSVC\$ver"
    [pscustomobject]@{
        Bin     = Join-Path $msvc 'bin\Hostx64\x64'
        Include = Join-Path $msvc 'include'
        Lib     = Join-Path $msvc 'lib\x64'
    }
}

$Wdk = Join-Path $Packages "Microsoft.Windows.WDK.x64.$WdkVersion\c"
$Sdk = Join-Path $Packages "Microsoft.Windows.SDK.CPP.$WdkVersion\c"
$KmInc = Join-Path $Wdk "Include\$KitVersion"
$KmLib = Join-Path $Wdk "Lib\$KitVersion\km\x64"
$KmdfInc = Join-Path $Wdk "Include\wdf\kmdf\$KmdfVersion"
$KmdfLib = Join-Path $Wdk "Lib\wdf\kmdf\x64\$KmdfVersion"
$SdkBin = Join-Path $Sdk "bin\$KitVersion\x64"
$WdkBin = Join-Path $Wdk "bin\$KitVersion"
$WdkTools = Join-Path $Wdk "tools\$KitVersion\x64"
$SdkX64 = Join-Path $Packages "Microsoft.Windows.SDK.CPP.x64.$WdkVersion\c"

$Sources = @(
    'src\Main\adapter.cpp',
    'src\Main\basetopo.cpp',
    'src\Main\common.cpp',
    'src\Main\loopback.cpp',
    'src\Main\mintopo.cpp',
    'src\Main\minwavert.cpp',
    'src\Main\minwavertstream.cpp',
    'src\Main\NewDelete.cpp',
    'src\Filters\micarraytopo.cpp',
    'src\Filters\speakertopo.cpp',
    'src\Utilities\hw.cpp',
    'src\Utilities\kshelper.cpp'
)

function Build-Driver {
    param($Msvc)
    New-Item -ItemType Directory -Force $ObjDir, $OutDir | Out-Null
    $kitIncludes = @((Join-Path $KmInc 'km\crt'), (Join-Path $KmInc 'km'), (Join-Path $KmInc 'shared'), $KmdfInc)
    $includes = @((Join-Path $Root 'src\Inc'), (Join-Path $Root 'src\Filters'), (Join-Path $Root 'src\Utilities'), (Join-Path $Root 'src\Main'))
    $defines = @(
        '_WIN64', '_AMD64_', 'AMD64', 'DEPRECATE_DDK_FUNCTIONS=1', 'WINNT=1', '_WIN32_WINNT=0x0A00', 'WINVER=0x0A00',
        "NTDDI_VERSION=$NtddiVersion", 'POOL_NX_OPTIN=1', 'POOL_ZERO_DOWN_LEVEL_SUPPORT',
        "KMDF_VERSION_MAJOR=1", "KMDF_VERSION_MINOR=$($KmdfVersion.Split('.')[1])",
        '_WIN32', 'UNICODE', '_UNICODE', 'PC_IMPLEMENTATION', 'DEBUG_LEVEL=DEBUGLVL_TERSE',
        '_USE_WAVERT_', '_USE_IPortClsRuntimePower', '_NEW_DELETE_OPERATORS_'
    )
    if ($Configuration -eq 'Debug') { $defines += @('DBG=1', 'MSC_NOOPT') }
    $cflags = @(
        '/nologo', '/c', '/kernel', '/W4', '/WX', '/Zi', '/Gz', '/GS', '/Gy', '/Gw', '/GR-', '/Zp8', '/Zc:wchar_t-',
        '/Zc:forScope', '/Zc:inline', '/fp:precise', '/Oy-', '/guard:cf', '/d2epilogunwind', '/cbstring', '/FS',
        "/Fd$ObjDir\PlanckVAD.pdb"
    )
    if ($Configuration -eq 'Release') { $cflags += @('/Ox', '/Os', '/GF', '/d1nodatetime') } else { $cflags += @('/Od') }
    $cflags += @('/external:W0', '/wd4603', '/wd4627', '/wd4986', '/wd4987')
    $cflags += $kitIncludes | ForEach-Object { "/external:I$_" }
    $cflags += $includes | ForEach-Object { "/I$_" }
    $cflags += $defines | ForEach-Object { "/D$_" }

    $objs = @()
    foreach ($src in $Sources) {
        $obj = Join-Path $ObjDir ([IO.Path]::GetFileNameWithoutExtension($src) + '.obj')
        Invoke-Tool (Join-Path $Msvc.Bin 'cl.exe') ($cflags + @("/Fo$obj", (Join-Path $Root $src)))
        $objs += $obj
    }

    $res = Join-Path $ObjDir 'PlanckVAD.res'
    $sdkInc = Join-Path $Sdk "Include\$KitVersion"
    $rcIncludes = @((Join-Path $sdkInc 'um'), (Join-Path $sdkInc 'shared')) | ForEach-Object { "/i$_" }
    $v = $Version.Split('.')
    $rcDefines = @("/DPLANCK_VERSION_MAJOR=$($v[0])", "/DPLANCK_VERSION_MINOR=$($v[1])", "/DPLANCK_VERSION_PATCH=$($v[2])",
        "/DPLANCK_VERSION_BUILD=$($v[3])", "/DPLANCK_VERSION_STR=\`"$Version\`"")
    Invoke-Tool (Join-Path $SdkBin 'rc.exe') (@('/nologo', "/fo$res") + $rcIncludes + $rcDefines + @((Join-Path $Root 'src\Main\PlanckVAD.rc')))

    $libs = @(
        (Join-Path $KmLib 'BufferOverflowFastFailK.lib'), (Join-Path $KmLib 'ntoskrnl.lib'), (Join-Path $KmLib 'hal.lib'),
        (Join-Path $KmLib 'wmilib.lib'), (Join-Path $KmdfLib 'WdfLdr.lib'), (Join-Path $KmdfLib 'WdfDriverEntry.lib'),
        (Join-Path $KmLib 'portcls.lib'), (Join-Path $KmLib 'stdunk.lib'), (Join-Path $KmLib 'libcntpr.lib'), (Join-Path $KmLib 'ksguid.lib')
    )
    $lflags = @(
        '/nologo', '/DRIVER', '/kernel', '/SUBSYSTEM:NATIVE,10.00', '/osversion:10.0', '/NODEFAULTLIB', '/ENTRY:FxDriverEntry',
        '/MERGE:_TEXT=.text;_PAGE=PAGE', '/SECTION:INIT,d', '/DEBUG', '/debugtype:pdata', '/RELEASE', '/OPT:REF', '/OPT:ICF',
        '/INTEGRITYCHECK', '/guard:cf', '/DYNAMICBASE', '/NXCOMPAT', '/MACHINE:X64',
        '/IGNORE:4198,4010,4037,4039,4065,4070,4078,4087,4089,4221,4108,4088,4218,4235',
        "/OUT:$OutDir\PlanckVAD.sys", "/PDB:$OutDir\PlanckVAD.pdb", '/PDBALTPATH:%_PDB%'
    )
    Invoke-Tool (Join-Path $Msvc.Bin 'link.exe') ($lflags + $objs + @($res) + $libs)
}

function Test-Loopback {
    param($Msvc)
    $testDir = Join-Path $BuildDir 'tests'
    New-Item -ItemType Directory -Force $testDir | Out-Null
    $sdkInc = Join-Path $Sdk "Include\$KitVersion"
    $sdkLib = $SdkX64
    $saved = $env:INCLUDE, $env:LIB
    try {
        $env:INCLUDE = @($Msvc.Include, (Join-Path $sdkInc 'ucrt'), (Join-Path $sdkInc 'um'), (Join-Path $sdkInc 'shared')) -join ';'
        $env:LIB = @($Msvc.Lib, (Join-Path $sdkLib 'ucrt\x64'), (Join-Path $sdkLib 'um\x64')) -join ';'
        $exe = Join-Path $testDir 'loopback_test.exe'
        Invoke-Tool (Join-Path $Msvc.Bin 'cl.exe') @('/nologo', '/EHsc', '/std:c++17', '/W4', '/WX', '/Zi', '/MTd', '/RTC1', '/sdl', "/I$(Join-Path $Root 'src\Inc')",
            "/Fo$testDir\", "/Fd$testDir\loopback_test.pdb", "/Fe$exe", (Join-Path $Root 'tests\loopback_test.cpp'))
        Invoke-Tool $exe @()
    } finally {
        $env:INCLUDE, $env:LIB = $saved
    }
}

function New-Package {
    if (Test-Path $PackageDir) { Remove-Item -Recurse -Force $PackageDir }
    New-Item -ItemType Directory -Force $PackageDir | Out-Null
    Copy-Item (Join-Path $OutDir 'PlanckVAD.sys') $PackageDir
    $inf = Join-Path $PackageDir 'PlanckVAD.inf'
    Copy-Item (Join-Path $Root 'src\Main\PlanckVAD.inx') $inf
    Write-Host '== ApiValidator (Universal DDIs) =='
    $ddis = Join-Path $Wdk "build\$KitVersion\universalDDIs\x64"
    Invoke-Tool (Join-Path $WdkBin 'x64\apivalidator.exe') @("-DriverPackagePath:$PackageDir", "-SupportedApiXmlFiles:$(Join-Path $ddis 'UniversalDDIs.xml')",
        "-ModuleWhiteListXmlFiles:$(Join-Path $ddis 'ModuleWhitelist.xml')")
    Invoke-Tool (Join-Path $WdkBin 'x64\stampinf.exe') @('-f', $inf, '-a', 'amd64', '-d', '*', '-v', $Version, '-k', $KmdfVersion)
    Write-Host '== InfVerif /w (Windows driver requirements) =='
    Invoke-Tool (Join-Path $WdkTools 'infverif.exe') @('/v', '/w', $inf)
    Write-Host '== InfVerif /u (Universal driver requirements) =='
    Invoke-Tool (Join-Path $WdkTools 'infverif.exe') @('/v', '/u', $inf)
}

function New-Catalog {
    Write-Host '== Inf2Cat =='
    Invoke-Tool (Join-Path $WdkBin 'x86\Inf2Cat.exe') @("/driver:$PackageDir", "/os:$CatalogOs", '/uselocaltime')
}

# Makes a throwaway code-signing certificate, signs with it, and deletes the
# certificate and its private key: only the public .cer is kept, for a test
# machine's trust store. signtool signs from Cert:\CurrentUser\My by
# thumbprint, because signing from a PFX imports a persistent copy of the key
# into the user's key storage provider on every call. signtool can also copy
# the certificate into Cert:\CurrentUser\CA, so that copy is removed too.
function Invoke-TestSign {
    param([string[]]$Files)
    $signDir = Join-Path $BuildDir 'testsign'
    New-Item -ItemType Directory -Force $signDir | Out-Null
    $cer = Join-Path $signDir 'PlanckVAD-test.cer'
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Planck VAD Test Signing (ephemeral)' `
        -CertStoreLocation Cert:\CurrentUser\My -KeyExportPolicy NonExportable -HashAlgorithm SHA256 -NotAfter (Get-Date).AddDays(30)
    $thumb = $cert.Thumbprint
    $key = [Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPrivateKey($cert)
    $keyName = $key.Key.KeyName
    $keyProvider = $key.Key.Provider
    $key.Dispose()
    try {
        Export-Certificate -Cert $cert -FilePath $cer | Out-Null
        Invoke-Tool (Join-Path $SdkBin 'signtool.exe') (@('sign', '/fd', 'SHA256', '/s', 'My', '/sha1', $thumb) + $Files)
    } finally {
        Remove-Item "Cert:\CurrentUser\My\$thumb" -DeleteKey
        foreach ($store in 'My', 'CA') {
            if (Test-Path "Cert:\CurrentUser\$store\$thumb") { Remove-Item "Cert:\CurrentUser\$store\$thumb" }
        }
    }
    if ([Security.Cryptography.CngKey]::Exists($keyName, $keyProvider)) {
        throw "The test-signing private key $keyName is still in $keyProvider"
    }
    $thumb
}

# signtool verify always ends in CERT_E_UNTRUSTEDROOT for a certificate no
# trust store holds, and trusting it would change this machine. So this
# accepts exactly that outcome, after signtool has checked the signature and
# the catalog hashes, and checks the signer is the ephemeral certificate.
function Test-TestSignature {
    param([string]$Thumbprint)
    $sys = Join-Path $PackageDir 'PlanckVAD.sys'
    $cat = Join-Path $PackageDir 'PlanckVAD.cat'
    $inf = Join-Path $PackageDir 'PlanckVAD.inf'
    $checks = @(
        @('verify', '/pa', '/v', $sys),
        @('verify', '/pa', '/v', $cat),
        @('verify', '/pa', '/v', '/c', $cat, $sys),
        @('verify', '/pa', '/v', '/c', $cat, $inf)
    )
    foreach ($check in $checks) {
        Write-Host "== signtool $($check -join ' ') =="
        # cmd merges stderr so PowerShell 5 does not wrap it in error records.
        $line = (@((Join-Path $SdkBin 'signtool.exe')) + $check | ForEach-Object { '"' + $_ + '"' }) -join ' '
        $out = cmd.exe /d /c "$line 2>&1" | Out-String
        Write-Host $out
        $untrustedOnly = $LASTEXITCODE -eq 1 -and $out -match 'terminated in a root\s+certificate which is not trusted' -and
            $out -match 'Number of errors: 1' -and $out -notmatch 'not signed|hash.*not.*match|not found in|0x80096010|0x800B0100'
        if ($LASTEXITCODE -ne 0 -and -not $untrustedOnly) { throw 'signtool verify failed for a reason other than the untrusted test root' }
    }
    foreach ($f in @($sys, $cat)) {
        $sig = Get-AuthenticodeSignature $f
        if ($sig.SignerCertificate.Thumbprint -ne $Thumbprint) { throw "$f is not signed by the ephemeral test certificate" }
        Write-Host "$f signed by $($sig.SignerCertificate.Subject) ($Thumbprint)"
    }
}

if ($Clean -and (Test-Path $BuildDir)) { Remove-Item -Recurse -Force $BuildDir }
Restore-Kits
$msvc = Find-Msvc
Test-Loopback $msvc
Build-Driver $msvc
New-Package
New-Catalog
if ($TestSign) {
    # The catalog records the driver's Authenticode hash, which excludes the
    # signature, so the .sys can be signed after the catalog is made.
    $thumb = Invoke-TestSign @((Join-Path $PackageDir 'PlanckVAD.sys'), (Join-Path $PackageDir 'PlanckVAD.cat'))
    Test-TestSignature $thumb
}
Write-Host "Driver package: $PackageDir"
