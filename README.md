# TailConnect

**One-click access to all your computers, from anywhere.** TailConnect lives in your menu bar / system tray and connects your devices over a private, encrypted [Tailscale](https://tailscale.com) network that it runs itself. Open a terminal, VS Code or Cursor on another machine, forward its web apps to `localhost`, and watch its CPU, memory and GPU — without setting up SSH keys, VPNs or port forwarding by hand.

Think of it as NVIDIA Sync for every computer you own.

> **Status:** Phase 1 (device access) — preview for macOS, Windows and Linux.
> Phase 2 adds browsing other devices' files directly in Finder / File Explorer.

## Download

**One click:**

| macOS 13+ (Apple Silicon & Intel) | Windows 10/11 | Linux (x64 / ARM) |
|:---:|:---:|:---:|
| [**⬇ Download for Mac**](https://github.com/imbikramsaha/TailConnect/releases/latest/download/TailConnect-macos.dmg) | [**⬇ Download for Windows**](https://github.com/imbikramsaha/TailConnect/releases/latest/download/TailConnect-windows-setup.exe) | [.deb x64](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_amd64.deb) · [.deb ARM](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_arm64.deb)<br>[.rpm x64](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_x86_64.rpm) · [.rpm ARM](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_aarch64.rpm) |

**Or one command:**

macOS and Linux (Terminal):

```sh
curl -fsSL https://github.com/imbikramsaha/TailConnect/releases/latest/download/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://github.com/imbikramsaha/TailConnect/releases/latest/download/install.ps1 | iex
```

The commands detect your system, download the right file, **verify its checksum** (and TailConnect's release signature wherever OpenSSL 3 is available — most Linux systems, Macs with Homebrew, Windows with Git), install it, and start TailConnect. No administrator rights are needed on Windows; on Linux the package manager asks for your password.

<details>
<summary>Other ways to install</summary>

- **Homebrew (macOS):** `brew tap imbikramsaha/tap && brew trust imbikramsaha/tap && brew install --cask tailconnect`
- **Windows portable zip:** [TailConnect-windows-amd64.zip](https://github.com/imbikramsaha/TailConnect/releases/latest/download/TailConnect-windows-amd64.zip)
- **Linux, any distribution (no root):** [x64 .tar.gz](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_amd64.tar.gz) · [ARM .tar.gz](https://github.com/imbikramsaha/TailConnect/releases/latest/download/tailconnect_linux_arm64.tar.gz) — or the `install.sh` command above, which falls back to this automatically
- **A specific version:** all files are on the [Releases page](https://github.com/imbikramsaha/TailConnect/releases); the install commands accept `TAILCONNECT_VERSION=0.3.1`
- **Check a download yourself:** `gh attestation verify <file> --repo imbikramsaha/TailConnect` proves it was built by this repository's CI.

</details>

> First launch of a preview build: on **macOS**, if you downloaded the DMG in a browser, right-click TailConnect → **Open** → **Open** once. On **Windows**, SmartScreen may show **More info → Run anyway**. (The install commands avoid both prompts.)

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

## After installing

- **macOS:** TailConnect lives in the menu bar. For screen sharing *to* a Mac, turn on **System Settings → General → Sharing → Screen Sharing**. The install command also adds the `tailconnect` command.
- **Windows:** TailConnect lives in the system tray and the Start menu; `tailconnect` is on your PATH in new terminals.
- **Linux:** the native window needs WebKitGTK 4.1 (installed automatically with the .deb/.rpm); without it TailConnect opens in your browser. On GNOME, the tray icon needs the *AppIndicator* extension (preinstalled on Ubuntu).
- **Servers / headless Linux:** run `tailconnect --headless` (prints a sign-in URL) or `tailconnect --headless --authkey-file /path/to/key` for unattended setup.
- **Uninstall:** Windows *Settings → Apps*, `brew uninstall --cask tailconnect`, or drag the app to the Trash (macOS); on Linux run `tailconnect cleanup` then remove the package.

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

See [SECURITY.md](SECURITY.md) for the full model and how to report a vulnerability. In short:

- **Two factors for every device.** All traffic travels inside Tailscale's WireGuard tunnels, and callers are identified by Tailscale (`WhoIs`). On top of that, every request is signed with the device's TailConnect key, so another program or user account sharing a machine's Tailscale connection can't act as that device. A device never accepts itself as a caller.
- **Access levels**, enforced on every request: *Full access* (terminal, editors, files), *Ports & monitor*, or *Monitor only*, optionally limited to specific ports. Revoking or lowering access closes open tunnels and SSH sessions immediately, and revoked devices aren't auto-paired again.
- **Pairing.** Devices on your own Tailscale account pair automatically (you're notified each time). Others need your approval, with a 6-digit code shown on both screens. A paired device presenting a new key needs approval again.
- **Built-in SSH** answers only devices with full access, verified by tailnet identity *and* paired key; its host key is pinned over the authenticated API. Keys installed for the system SSH server only work through TailConnect's tunnel (`from="127.0.0.1,::1"`).
- **Nothing a peer sends is trusted:** names, usernames, paths and addresses are validated before reaching `~/.ssh/config`, command lines, notifications or the UI.
- **Local control API:** loopback only, Host-header and same-origin checks, a per-run token for the CLI (which first verifies it's talking to the real app), and per-window sessions sent as headers — never cookies, which other local web servers could receive.
- **Signed updates:** the app installs an update only if its checksums carry the maintainer's offline signature (and, for Developer-ID builds, the same Apple team). CI can build releases but cannot sign them.
- **Uninstalling** removes TailConnect's keys from `~/.ssh/authorized_keys` and its `~/.ssh/config` entry (Windows uninstaller, Homebrew). On Linux run `tailconnect cleanup` before removing the package.
- TailConnect never stores SSH passwords, and shows a new host's fingerprint for you to confirm before sending one.

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

Set `SIGN_IDENTITY` (and `NOTARY_PROFILE`, `TEAM_ID`) to sign and notarize for distribution.

### Releasing

1. Push a tag `vX.Y.Z`. CI tests everything, builds all packages and creates a **draft** release.
2. Run `scripts/publish-release.sh vX.Y.Z` on the maintainer's machine. It verifies the draft's checksums, signs `SHA256SUMS` with the offline release key (`~/.tailconnect-release/release.key`) and publishes. Apps refuse updates without that signature.
3. Copy the release's `tailconnect.rb` into the Homebrew tap.

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
