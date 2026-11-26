# Build Planck from this checkout and install it, for local development.
#
#   powershell -ExecutionPolicy Bypass -File scripts\install-windows.ps1 [options]
#
# The exes are built into a temporary folder, then copied into -InstallDir.
# A running planck-client.exe from -InstallDir is stopped first. Unless
# -NoHostTask is given, planck-host.exe runs from a Scheduled Task (-HostTaskName)
# started as the current user through run-host.cmd, so it outlives this shell.
#
#   -InstallDir           default $env:LOCALAPPDATA\Programs\Planck
#   -NoShortcut           no Start Menu shortcut
#   -HostTaskName         default "Planck Host"
#   -HostArgs             planck-host arguments, default "-listen :41001 -media-port 41002 -discovery-port 41000"
#   -NoHostTask           leave the Scheduled Task and any running planck-host.exe alone
#   -LaunchClient         start planck-client.exe afterwards
#   -RegenerateResources  run `go run .\scripts\winres generate` even if the .syso files exist
#   -SignCert, -SignPassword  sign both exes with signtool (a .pfx file and its password)
param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Planck'),
    [switch]$NoShortcut,
    [string]$HostTaskName = 'Planck Host',
    [string]$HostArgs = '-listen :41001 -media-port 41002 -discovery-port 41000',
    [switch]$NoHostTask,
    [switch]$LaunchClient,
    [switch]$RegenerateResources,
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

function Get-ProcessesUnder([string]$name, [string[]]$dirs) {
    $prefixes = $dirs | Where-Object { $_ } | ForEach-Object { $_.TrimEnd('\') + '\' }
    Get-CimInstance Win32_Process -Filter "Name='$name'" | Where-Object {
        $exe = $_.ExecutablePath
        $exe -and ($prefixes | Where-Object { $exe.StartsWith($_, [StringComparison]::OrdinalIgnoreCase) })
    }
}

function Stop-ProcessesUnder([string]$name, [string[]]$dirs) {
    $procs = @(Get-ProcessesUnder $name $dirs)
    if ($procs.Count -eq 0) { return }
    Write-Host "==> stopping $name"
    foreach ($p in $procs) { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue }
    $deadline = (Get-Date).AddSeconds(10)
    while ((Get-ProcessesUnder $name $dirs) -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 100 }
}

$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)

$version = (Select-String -Path internal\version\version.go -Pattern '^var Version = "(.+)"$' | Select-Object -First 1).Matches[0].Groups[1].Value
if (-not $version) { throw 'no version in internal\version\version.go' }
$commit = (git rev-parse --short=12 HEAD 2>$null)
if (-not $commit) { $commit = 'unknown' }
$buildDate = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
$pkg = 'github.com/planckdotgg/planck/internal/version'
$ldflags = "-s -w -X $pkg.Commit=$commit -X $pkg.BuildDate=$buildDate"

$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

$syso = @('cmd\planck-client\rsrc_windows_amd64.syso', 'cmd\planck-host\rsrc_windows_amd64.syso')
if ($RegenerateResources -or ($syso | Where-Object { -not (Test-Path $_) })) {
    Invoke-Checked 'icon and version resources' { go run .\scripts\winres generate }
}

$signtool = $null
if ($SignCert) {
    if (-not (Test-Path $SignCert)) { throw "the signing certificate $SignCert does not exist" }
    $signtool = (Get-Command signtool.exe -ErrorAction SilentlyContinue).Source
    if (-not $signtool) {
        $signtool = (Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -Last 1).FullName
    }
    if (-not $signtool) { throw 'a signing certificate was given, but signtool.exe was not found' }
}

$stage = Join-Path ([System.IO.Path]::GetTempPath()) ("planck-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $stage | Out-Null
try {
    $client = Join-Path $stage 'planck-client.exe'
    $hostExe = Join-Path $stage 'planck-host.exe'
    Invoke-Checked "planck-client $version" { go build -trimpath -tags mygo_noinspector "-ldflags=$ldflags -H=windowsgui" -o $client .\cmd\planck-client }
    Invoke-Checked "planck-host $version" { go build -trimpath "-ldflags=$ldflags" -o $hostExe .\cmd\planck-host }
    Invoke-Checked 'check planck-client resources' { go run .\scripts\winres check -version $version $client }
    Invoke-Checked 'check planck-host resources' { go run .\scripts\winres check -version $version $hostExe }
    if ($signtool) {
        foreach ($f in @($client, $hostExe)) {
            Invoke-Checked "sign $(Split-Path -Leaf $f)" {
                & $signtool sign /f $SignCert /p $SignPassword /fd sha256 /tr http://timestamp.digicert.com /td sha256 $f
            }
        }
    }

    New-Item -ItemType Directory -Force $InstallDir | Out-Null
    Stop-ProcessesUnder 'planck-client.exe' @($InstallDir)

    if (-not $NoHostTask) {
        $searchDirs = @($InstallDir)
        $task = Get-ScheduledTask -TaskName $HostTaskName -ErrorAction SilentlyContinue
        if ($task) {
            foreach ($a in $task.Actions) {
                if ("$($a.Arguments)" -match '(?i)([A-Za-z]:\\[^"]*?)\\run-host\.cmd') { $searchDirs += $Matches[1] }
            }
            Write-Host "==> stopping task $HostTaskName"
            schtasks /End /TN $HostTaskName 2>&1 | Out-Null
        }
        Stop-ProcessesUnder 'planck-host.exe' $searchDirs
    }

    Write-Host "==> install $InstallDir"
    Copy-Item $client, $hostExe $InstallDir -Force
    Copy-Item LICENSE (Join-Path $InstallDir 'LICENSE.txt') -Force
    Copy-Item THIRD_PARTY_NOTICES.md $InstallDir -Force

    if (-not $NoHostTask) {
        $cmdPath = Join-Path $InstallDir 'run-host.cmd'
        $lines = @(
            '@echo off',
            "cd /d `"$InstallDir`"",
            "`"$InstallDir\planck-host.exe`" $HostArgs > `"$InstallDir\planck-host.out.log`" 2> `"$InstallDir\planck-host.err.log`""
        )
        [System.IO.File]::WriteAllText($cmdPath, (($lines -join "`r`n") + "`r`n"), [System.Text.Encoding]::ASCII)
        $action = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument "--headless cmd.exe /d /c `"$cmdPath`"" -WorkingDirectory $InstallDir
        $user = "$env:USERDOMAIN\$env:USERNAME"
        $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
        Write-Host "==> task $HostTaskName"
        Register-ScheduledTask -TaskName $HostTaskName -Action $action -Principal $principal -Settings $settings -Force | Out-Null
    }

    if (-not $NoShortcut) {
        $lnk = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs\Planck.lnk'
        $shell = New-Object -ComObject WScript.Shell
        $sc = $shell.CreateShortcut($lnk)
        $sc.TargetPath = Join-Path $InstallDir 'planck-client.exe'
        $sc.WorkingDirectory = $InstallDir
        $sc.IconLocation = "$(Join-Path $InstallDir 'planck-client.exe'),0"
        $sc.Save()
        Write-Host "==> shortcut $lnk"
    }

    if (-not $NoHostTask) {
        Invoke-Checked "start task $HostTaskName" { schtasks /Run /TN $HostTaskName }
    }
    if ($LaunchClient) {
        Write-Host '==> launching planck-client'
        Start-Process (Join-Path $InstallDir 'planck-client.exe') -WorkingDirectory $InstallDir
    }
    Write-Host "==> installed $version to $InstallDir"
} finally {
    Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
}
