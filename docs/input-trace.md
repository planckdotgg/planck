# Input and cursor trace

A JSON-lines record of what the client and the host do with the mouse and
the cursor, for bugs where the cursor jumps, sticks or goes astray. Off by
default; off, it costs an atomic load per event.

## Turning it on

| Side | How |
|---|---|
| Client app | Settings › General › Diagnostics › **Trace input and the cursor** (kept in `client.json` as `inputTrace`), or `planck-client -input-trace`, or `PLANCK_INPUT_TRACE=1` for one run |
| Host | Follows the client: on while a client that traces streams from it, off when it stops or its stream ends. `planck-host -input-trace` or `PLANCK_INPUT_TRACE=1` traces every session whatever the client asks. The app's own host follows the app's `-input-trace` |

A host older than this feature never hears the request (`wire.FeatureInputTrace`), and the client traces alone.

## Files

`<config dir>/logs/input-trace-client-YYYYMMDD-HHMMSS.jsonl` and
`input-trace-host-...jsonl`; the config dir is `~/Library/Application Support/planck`
on macOS, `%AppData%\planck` on Windows and `~/.config/planck` on Linux, or `-config`. Each file rotates at
20 MB; the newest five of each side stay. The Settings row shows the file
written now, with a button that copies its path.

## Records

Every line has `ts` (UTC), `t_ns` (the process's `transport.Mono` clock,
as the latency traces use), `side` and `kind`.

| Kind | Side | What |
|---|---|---|
| `trace_start`, `trace_stop`, `trace_dropped` | both | the file's start and end; records a full queue dropped |
| `session_meta` | client | window and content bounds, pixels, scale, display, full screen, video and drawable size, the picture's place in the view |
| `host_cursor_state` | client | each `CursorState` from the host, with a sequence number and the last shape ID |
| `pointer_mode` | client | each change of the lock: from, to, reason, the host's cursor, the warp target and whether the system made it |
| `pointer_sys` | client | hold, free, hide, show, and the platform calls under them: `ClipCursor`, `SetCursorPos`, `ShowCursor` (Windows), `CGWarpMouseCursorPosition`, `CGAssociateMouseAndMouseCursorPosition` (macOS), with return values |
| `client_input` | client | the pointer as MyGo had it and what went to the host (`abs`, `rel`, `button`, `wheel`, or `suppressed` with why), written when it changes |
| `host_meta` | host | captured display, input desktop, virtual screen, clip, cursor and DPI, as tracing starts and each video opens |
| `cursor_poll` | host | each change of the system cursor (visible, captured, place, `hcursor`, clip, foreground window and process) with what the report did: `emit`, `hold_hide`, `drop_flicker`, `same` |
| `host_input` | host | each mouse input from the client, the `SendInput` calls it made (flags, dx, dy, data) and the cursor before and after |
| `cursor_jump` | host | the cursor moved over 2 px with no injection of the host's to move it, between two injections (`input_gap`) or two polls (`poll`): a game's `SetCursorPos`, or the host's own mouse |

Linux traces the lock's hold and free but not the X11 or Wayland calls under them.
