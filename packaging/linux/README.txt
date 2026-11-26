Planck __VERSION__ for Linux (__ARCH__)

Planck streams a PC's desktop and games to another device with the least
delay it can. This archive holds the client and the host for Linux, both
static Go programs with no C library to match.

  bin/planck-client    the client: opens the Planck window (-headless for a
                       benchmark with no window, -version, -help)
  bin/planck-host      the host (-version, -help)
  share/               desktop entry, hicolor icons, the udev rule and the
                       systemd user unit
  install.sh           installs all of it for your user

Install for your user, to ~/.local:

  ./install.sh              the programs, desktop entry and icons
  ./install.sh --systemd    also a systemd user service for the host
  ./install.sh --prefix DIR install under DIR instead of ~/.local

Both X.Org and Wayland sessions work. GTK 3 draws the client window and
picks the display server by itself. The programs load these system
libraries when they need them; install the packages your distribution
names for them and a missing one is reported by name:

  libgtk-3-0 and libepoxy   the client window
  libpulse (PulseAudio) or pipewire-pulse   audio, capture and virtual mic
  libX11 and libXtst        host input on X.Org when /dev/uinput is closed
  wl-clipboard (Wayland) or xclip / xsel (X.Org)   clipboard sharing
  xdg-desktop-portal        host input on Wayland when /dev/uinput is closed

Host input. The host injects keys, mouse and gamepads through /dev/uinput,
which works on X.Org and Wayland. Your user needs access to it:

  sudo cp share/planck/60-planck-uinput.rules /etc/udev/rules.d/
  echo uinput | sudo tee /etc/modules-load.d/uinput.conf
  sudo modprobe uinput
  sudo udevadm control --reload-rules
  sudo udevadm trigger --name-match=uinput

then log in again. Without it, the host falls back to X11 XTest on X.Org,
and to the RemoteDesktop portal (which asks for your permission) on
Wayland; the portal moves the mouse by relative motion only. Set
PLANCK_INJECT_BACKEND to uinput, xtest or portal to force one.

Host as a service. install.sh --systemd puts planck-host.service in
~/.config/systemd/user. Start it with:

  systemctl --user enable --now planck-host

It starts with your graphical session, so the host sees your display.

Gamepads. A client reads controllers from /dev/input/event*; the desktop
grants the logged-in user access. The host presents virtual Xbox 360
controllers through uinput.

Planck is Apache-2.0 (LICENSE). Third-party notices: THIRD_PARTY_NOTICES.md.
https://planck.gg  https://github.com/planckdotgg/planck
