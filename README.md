# Display Switch for DankMaterialShell

A GNOME-style **Super+P** display switcher for [niri](https://github.com/YaLTeR/niri), as a DMS plugin. A centered popup in the DMS style lets you pick how your screens are used; a small Go backend does the work through the niri IPC.

- **PC screen only**: the built-in screen is used, external displays are turned off.
- **External only**: external displays only. When they are all unplugged the built-in screen comes back automatically, and it switches back to external-only when they are plugged again.
- **Duplicate**: the same picture on every display (uses `wl-mirror`, since niri has no native mirroring).
- **Extend**: every display is part of one larger desktop.

The popup works like GNOME's: press the shortcut again (or `P`, arrows, `Tab`) to cycle, `1`–`4` to pick directly, `Enter` to apply, `Esc` to close. Releasing Super after cycling applies the selection (can be turned off in the settings).

## Disclaimer

This project was created entirely by Claude Sonnet 5.5. It is provided "as is", without warranties of any kind. Use it at your own risk; the author is not responsible for any damage, data loss, or other issues resulting from its use.

## Requirements

- DankMaterialShell 1.6.0 or newer.
- niri (the plugin refuses to start in another compositor).
- [`wl-mirror`](https://github.com/Ferdi265/wl-mirror), only for the Duplicate mode.
- Go 1.24 or newer to build the small backend for your machine. The backend is deliberately built locally; a prebuilt binary is not included.

## Install
`build.sh` also offers to install `wl-mirror` (Duplicate mode) with your distribution's package manager (pacman, apt, dnf, zypper, apk, xbps, emerge, nix). Use `./build.sh --yes` to accept without being asked, or `--no-mirror` to skip the question.

From DankLinux registry (coming soon):
- Install plugin
- Run `~/.config/DankMaterialShell/plugins/displayswitch/build.sh`
- In Settings → Plugins, enable **Display Switch**.
- Assign a keyboard shortcut (see below).
- Enjoy!

Or from github (make sure you have git installed):
```sh
git clone https://github.com/equisq/displayswitchDMS.git
cp -r ./displayswitchDMS/ ~/.config/DankMaterialShell/plugins/displayswitch/
~/.config/DankMaterialShell/plugins/displayswitch/build.sh
```
- In Settings → Plugins, click **Scan for Plugins** and enable **Display Switch**.
- Assign a keyboard shortcut (see below).
- Enjoy!

If the backend is missing or the session is not niri, DMS keeps the plugin disabled and shows the reason in a notification.

### Keyboard shortcut
To open the popup with Super+P, you can use ipc commands.
In Settings → Keyboard Shortcuts, click on the **+** button, then click on the **record** button and type your desired shortcut.
In type, select **Shell Command**, and type `dms ipc call displayswitch toggle`.

## How it works

```
niri ◀──IPC socket──▶ bin/displayswitch-dmsd ──JSON lines──▶ QML plugin
                       (Go; planner + controller)   $XDG_RUNTIME_DIR/dms-displayswitch.sock
                              │
                              └── wl-mirror (Duplicate mode)
```

The backend watches niri's outputs and computes the layout for the requested mode with a pure planner whose invariant is **never leave every screen off**. The controller enables the new outputs before disabling the old ones, waits for displays that are waking up, and has a safety net that re-enables everything if something goes wrong. The QML only displays state and sends the chosen mode.

The layout you chose is remembered per set of connected displays (identified by make, model and serial, so it does not depend on the port) in `~/.local/state/DankMaterialShell/displayswitch/state.json`, and restored at login.

niri has no hotplug event, so the backend reacts to workspace changes and polls lightly. A circuit breaker stops it from fighting a monitor that keeps reconnecting.

## IPC

```sh
dms ipc call displayswitch toggle      # open, or cycle if already open
dms ipc call displayswitch open
dms ipc call displayswitch close
dms ipc call displayswitch set extend  # internal | external | mirror | extend
dms ipc call displayswitch status
dms ipc call displayswitch rescue      # enable every display
```

## Edge cases

- **External unplugged in External only**: falls back to the PC screen and tells you; returns to external-only when a display is plugged back.
- **Last screen can't be turned off**: a mode that would leave nothing enabled is never applied.
- **Laptop without built-in screen / no external**: unavailable modes are greyed out with the reason.
- **New display plugged in**: extended by default (configurable); displays you already configured keep their saved layout.
- **niri config reload**: niri forgets temporary output changes, so the layout is re-applied.
- **wl-mirror crashes or is missing**: the displays are extended instead and a notification is shown.
- **Backend crash**: restarted with backoff by the plugin.
- **Stuck on a black screen**: `bin/displayswitch-dmsd rescue` (from a TTY if necessary) turns every output back on.

## Differences from GNOME

- Duplicate relies on `wl-mirror` windows fullscreened on each output, not on real mirroring in the compositor.
- Output changes are temporary in niri (as with `niri msg output`); they do not edit your config.
- No per-display resolution or scale editing.

## Development

- Build the backend on the target machine with `./build.sh`; the generated executable under `bin/` is machine-specific and should not be committed. `./build.sh --test` also runs the tests.
- Backend tests: `cd backend && go vet ./... && go test -race ./...`. They use a fake niri socket and a fake `wl-mirror`.
- Exit codes of `displayswitch-dmsd`: 3 no daemon running, 4 mode unavailable, 5 `wl-mirror` missing, 6 not a niri session.

## Contributing

Contributions are welcome. Please use this workflow so changes are easy to review:

1. Start from the latest default branch. If you do not have write access, fork the repository first.
2. Create a separate branch for each change. Use a short, descriptive name with a type prefix, such as `feat/add-mode`, `fix/hotplug-race`, or `docs/update-install-steps`. Do not work directly on the default branch.
3. Make focused changes that follow the style and patterns already used in the project.
4. Test your changes before opening a pull request. For backend or build changes, run `./build.sh --test` on the target machine. For UI changes, also check the affected plugin flow in DankMaterialShell under niri. If a check cannot be run, say why in the pull request.
5. Push your branch and open a pull request against the repository's default branch. Keep each pull request focused on one change and provide enough context for someone else to review it.
6. Respond to review feedback by pushing follow-up commits to the same branch.

### Pull request format

Use this structure in the pull request description:

```markdown
## Summary
<!-- What does this change do, and why? -->

## Changes
<!-- List the main changes. -->

## Testing
<!-- List the checks you ran and their results. If none, explain why. -->

## Checklist
- [ ] The change is focused and follows the existing project style.
- [ ] I tested the change, or explained why testing was not possible.
- [ ] I updated documentation or translations if needed.
- [ ] I checked that no generated or machine-specific build artifacts are included.
```


## Translations

The interface uses DMS `I18n.trFor("displaySwitch", ...)` and ships plugin-local catalogs in `translations/`. Available catalogs are `de.json`, `es.json`, `fr.json`, `it.json`, `ja.json`, `nl.json`, `pl.json`, `pt.json`, `ru.json`, `zh_CN.json`, and `zh_TW.json`. They cover German, Spanish, French, Italian, Japanese, Dutch, Polish, Portuguese, Russian, Simplified Chinese, and Traditional Chinese.

Translations were created by AI and may contain mistakes or inaccurate wording. Please check important details against the English source strings. DMS loads the catalog matching the active locale and refreshes plugin strings when the locale changes. English source strings remain the fallback for terms not yet translated in a catalog. Feel free to enhance existing translations or add new languages!

Plugin translations require DMS 1.6.0 or newer.
