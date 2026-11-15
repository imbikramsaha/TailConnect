# TailConnect

**One-click access to all your computers, from anywhere.** TailConnect lives in your menu bar / system tray and connects your devices over a private, encrypted [Tailscale](https://tailscale.com) network that it runs itself. Open a terminal, VS Code or Cursor on another machine, forward its web apps to `localhost`, and watch its CPU, memory and GPU — without setting up SSH keys, VPNs or port forwarding by hand.

Think of it as NVIDIA Sync for every computer you own.

> **Status:** Phase 1 (device access) — preview for macOS, Windows and Linux.
> Phase 2 adds browsing other devices' files directly in Finder / File Explorer.

## How it works

1. **Install** TailConnect on two or more computers.
2. **Sign in** once on each with the same account (Google, Microsoft, GitHub, Apple… via Tailscale; free).
3. Your devices **find each other and pair automatically.** That's it.

There's no Tailscale app to install — TailConnect embeds its own Tailscale node (it shows up as `tc-<name>` in your Tailscale admin console). If the Tailscale app is already running and signed in when you first open TailConnect, it simply uses that connection instead — no second sign-in.

## Features

| | |
|---|---|
| **Automatic pairing** | Devices on the same Tailscale account trust each other with zero prompts. Devices from other accounts send a request you approve — both screens show the same 6-digit code so you know it's the right device. Requests survive restarts. |
| **Access levels** | Grant each device **Full access** (terminal, editors, files), **Ports & monitor** (forwards, screen sharing, stats — no shell) or **Monitor only**, and optionally limit which ports it can reach. |
| **Built-in SSH** | Terminals and editors work without enabling an SSH server: TailConnect serves SSH itself, only over Tailscale, only to paired devices with full access (PTYs — ConPTY on Windows — `scp`/`sftp`, VS Code port forwarding). |
| **Uses your Tailscale app** | Shares the Tailscale app's connection when you have it (faster, one entry in your admin console), or runs its own. Switch any time in Settings. |
| **One-click apps** | Terminal, VS Code, VS Code Insiders, Cursor, Windsurf and Zed open a remote session on the device. |
| **Running services** | TailConnect lists what's listening on a device — Jupyter, Ollama, ComfyUI, dev servers… — with one-click **Open** (forward + browser). |
| **Custom apps & templates** | Forward any port to `localhost`, optionally starting it with a launch script. Templates for JupyterLab, Ollama, Open WebUI, ComfyUI, Stable Diffusion, code-server, TensorBoard, Streamlit, vLLM. Show each app on all or selected devices. |
| **Screen sharing** | Opens macOS Screen Sharing, Windows Remote Desktop or VNC/RDP on Linux through the private connection. |
| **Wake-on-LAN** | Wake a sleeping device from anywhere — another device of yours on the same home network sends the wake signal. |
| **Files & clipboard** | Drop files on a device (saved to `Downloads/TailConnect` there); send or fetch the clipboard. |
| **Resource monitor** | Live CPU per core, memory, NVIDIA GPU (utilisation, VRAM, temperature, power), disks, network; processes (with GPU memory) you can stop; Docker containers you can start/stop/restart. |
| **Alerts & notifications** | Native notifications for pairing requests, received files and alerts (GPU temperature, disk or memory nearly full). |
| **Connection test** | Shows whether traffic is direct, on the LAN or relayed, the latency and throughput, and what to do about a slow path. |
| **Any SSH host** | Add Linux/macOS machines without TailConnect — password used once to install a key, never stored. Services, processes, Docker, files and stats work over SSH. |
| **Works with your tools** | Every device gets an SSH alias (`ssh tc-desktop`) so `ssh`, `scp`, `rsync`, VS Code and JetBrains just work. |
| **Native app** | Menu-bar app with a native window (Dock icon while open, standard Edit shortcuts), auto-update from GitHub Releases, Homebrew cask. |
| **Activity log & reports** | See recent sessions, pairings and changes; "Report a problem" zips logs locally for a bug report. |
| **Self-hosting** | Point it at a [Headscale](https://headscale.net) server instead of Tailscale's. |

## Install

### macOS (13 Ventura or later, Apple Silicon or Intel)

```sh
brew install --cask imbikramsaha/tap/tailconnect
```

or download `TailConnect-<version>-macos.dmg` from [Releases](https://github.com/imbikramsaha/TailConnect/releases) and drag **TailConnect** to **Applications**. It appears in the menu bar and updates itself.

Preview builds are not notarized yet: the first time, right-click TailConnect → **Open** → **Open**.

Nothing else to enable: other devices reach your Mac through TailConnect's built-in SSH. (For screen sharing, turn on **System Settings → General → Sharing → Screen Sharing**.)

The Homebrew cask also installs the `tailconnect` command; with the DMG, link it yourself:

```sh
sudo ln -sf /Applications/TailConnect.app/Contents/MacOS/tailconnect /usr/local/bin/tailconnect
```

### Windows 10/11

Download `TailConnect-<version>-windows-setup.exe` from [Releases](https://github.com/imbikramsaha/TailConnect/releases) and run it — it installs for your user only (no administrator prompt), adds TailConnect to the Start menu and the `tailconnect` command to your PATH, and updates itself. A portable zip is also available. Until the builds are code-signed, SmartScreen may ask you to click **More info → Run anyway**.

### Linux

```sh
sudo apt install ./tailconnect_<version>_amd64.deb     # Debian/Ubuntu
sudo dnf install ./tailconnect-<version>.x86_64.rpm    # Fedora/RHEL
```

Packages for amd64 and arm64, plus a `.tar.gz`. The native window needs WebKitGTK 4.1 (`libwebkit2gtk-4.1-0`, installed automatically as a recommended package); without it TailConnect opens in your browser. On GNOME, the tray icon needs the *AppIndicator* extension (preinstalled on Ubuntu).

### Servers / headless Linux

```sh
tailconnect --headless            # prints a sign-in URL
tailconnect --headless --authkey tskey-auth-…   # unattended
```

## Command line

```
tailconnect status                 sign-in state and devices
tailconnect devices                list devices
tailconnect connect <device>       connect (name, alias or id)
tailconnect ssh <device>           SSH session
tailconnect pair list|approve|deny pairing requests
tailconnect open                   open the window
tailconnect quit
```

## Security model

- All device-to-device traffic travels inside Tailscale's WireGuard tunnels. Peers are identified by Tailscale (`WhoIs`), never by anything they claim.
- Every request is checked against the caller's access level on this device. Port tunnels only reach `127.0.0.1`/`::1`, and only allowed ports; the SSH port needs full access.
- Built-in SSH accepts a login only if the Tailscale identity *and* the key both match a device paired with full access. Lowering access or revoking closes its sessions; it is never reachable outside the tailnet.
- For the system SSH server, a device's key is in `~/.ssh/authorized_keys` (tagged `tailconnect:<node-id>`) only while it has full access.
- Pairing with other accounts shows a confirmation code on both screens. Process command lines are only shown to full-access devices. Stopping a process only works on your own user's processes.
- The local control API listens on `127.0.0.1` only, requires a per-run random token, rejects foreign `Host` headers (DNS rebinding) and requires a custom header on every change (CSRF). Windows sign in with single-use, 2-minute tickets, so the token never appears in URLs or process arguments.
- Updates are verified against the release's SHA-256 checksum and code signature (and, once Developer ID–signed, the same Apple Team ID) before installing.
- TailConnect never stores SSH passwords and uploads nothing except to Tailscale and GitHub (update checks).

## Files

| | macOS | Linux | Windows |
|---|---|---|---|
| Settings, logs, Tailscale state | `~/Library/Application Support/TailConnect` | `~/.config/TailConnect` | `%AppData%\TailConnect` |
| SSH key, known hosts, host aliases | `~/.ssh/tailconnect/` | same | same |

## Build from source

Requires Go 1.27+ (and Xcode command-line tools on macOS).

```sh
make test          # unit + two-node end-to-end tests (in-process Tailscale control server)
make run           # build and run for this machine
make macos         # dist/TailConnect.app + dist/TailConnect-<version>-macos.dmg (universal) + SHA256SUMS
scripts/build-windows.sh <v>   # TailConnectApp.exe + tailconnect.exe, zip, and the installer (Inno Setup, on Windows)
scripts/build-linux.sh <v>     # tar.gz, .deb, .rpm (on Linux; needs libgtk-3-dev libwebkit2gtk-4.1-dev)
scripts/update-cask.sh <version>   # Homebrew cask for the tap repo
```

Try the UI with two paired instances on an in-process tailnet:

```sh
TC_DEMO=/tmp/tc-demo go test ./internal/ui -run TestDemo -timeout 60m   # login links in /tmp/tc-demo/urls.txt
```

Set `SIGN_IDENTITY` (and `NOTARY_PROFILE`) to sign and notarize for distribution.

## Architecture

```
cmd/tailconnect        app entry point + CLI (+ `window`, the native window process)
internal/core          engine: discovery, pairing & access, connections, apps, services, wake, diagnostics, alerts, updates
internal/tailnet       Tailscale connection: embedded node (tsnet) or the Tailscale app (local API)
internal/agent         device-to-device API on the tailnet (:4747), checked per access level
internal/sshd          built-in SSH server on the tailnet (:4748): PTY, exec, SFTP, local forwarding
internal/hostops       local operations: services, processes, Docker, Wake-on-LAN, clipboard, files
internal/ui            local control API + embedded web UI (127.0.0.1)
internal/window        native window: WKWebView (macOS), WebView2 (Windows), WebKitGTK helper (Linux)
internal/tray          menu bar / system tray
internal/notify        system notifications
internal/update        GitHub Releases updater
internal/sshx, sshkeys, sshconf   SSH client, keys/trust, ~/.ssh/config integration
internal/launch        terminals, editors, browser, screen sharing; internal/sysinfo resource monitor
```

## Roadmap

- **Phase 1** — device access (this release): macOS ✅ · Windows ✅ · Linux ✅
- **Phase 2** — other devices' folders in Finder / File Explorer / Files
- Later — signed/notarized installers, mobile, teams

## License

TailConnect is licensed under the [Apache License 2.0](LICENSE).

TailConnect is an independent project and is not affiliated with Tailscale Inc. or NVIDIA.
