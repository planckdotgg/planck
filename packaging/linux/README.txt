Planck __VERSION__ for Linux (__ARCH__), command-line preview

This build is a preview. The desktop window and the capture, encode and
decode backends run on macOS and Windows; on Linux, planck-client and
planck-host build and run, but cannot open the desktop window or stream
yet. The tarball is here so packagers and contributors can track Linux.

  bin/planck-client    the client (-headless, -version, -help)
  bin/planck-host      the host (-version, -help)
  share/               a desktop entry and hicolor icons

To install for your user:

  cp bin/* ~/.local/bin/
  cp share/applications/planck.desktop ~/.local/share/applications/
  cp -r share/icons ~/.local/share/

Planck is Apache-2.0 (LICENSE). Third-party notices: THIRD_PARTY_NOTICES.md.
https://planck.gg  https://github.com/planckdotgg/planck
