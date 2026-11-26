; The Windows installer. scripts\bundle-windows.ps1 -Installer builds it and
; passes AppVersion, AppVersionNumeric (AppVersion without a -rc.1 style
; suffix, which VersionInfoVersion refuses), SourceDir (the app folder) and
; OutputDir.
;
; It installs for the current user by default, into
; %LOCALAPPDATA%\Programs\Planck, so it needs no administrator; the first
; dialog offers an install for all users. It installs no drivers: the
; virtual audio and display drivers are separate, optional installs.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef AppVersionNumeric
  #define AppVersionNumeric AppVersion
#endif
#ifndef SourceDir
  #define SourceDir "..\..\dist\windows-x64\Planck"
#endif
#ifndef OutputDir
  #define OutputDir "..\..\dist"
#endif

[Setup]
; Windows finds the installed app by this id for upgrades. Never change it.
AppId={{BECD0868-50DC-4CC9-8F60-630048643082}
AppName=Planck
AppVersion={#AppVersion}
AppVerName=Planck {#AppVersion}
AppPublisher=Planck
AppPublisherURL=https://planck.gg
AppSupportURL=https://github.com/planckdotgg/planck/issues
AppUpdatesURL=https://github.com/planckdotgg/planck/releases
DefaultDirName={autopf}\Planck
DefaultGroupName=Planck
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.19041
LicenseFile={#SourceDir}\LICENSE.txt
OutputDir={#OutputDir}
OutputBaseFilename=planck-{#AppVersion}-windows-x64-setup
SetupIconFile=..\..\assets\planck-windows.ico
UninstallDisplayIcon={app}\planck-client.exe
UninstallDisplayName=Planck
VersionInfoVersion={#AppVersionNumeric}
VersionInfoProductName=Planck
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
ShowLanguageDialog=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[Files]
Source: "{#SourceDir}\planck-client.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\planck-host.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\LICENSE.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\THIRD_PARTY_NOTICES.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Planck"; Filename: "{app}\planck-client.exe"
Name: "{autodesktop}\Planck"; Filename: "{app}\planck-client.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\planck-client.exe"; Description: "{cm:LaunchProgram,Planck}"; Flags: nowait postinstall skipifsilent

; Settings, paired devices and keys stay in %APPDATA%\planck after an
; uninstall, so a reinstall keeps its pairings.
