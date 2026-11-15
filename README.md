# TailConnect

**One-click access to all your computers, from anywhere.** TailConnect lives in your menu bar / system tray and connects your devices over a private, encrypted [Tailscale](https://tailscale.com) network that it runs itself. Open a terminal, VS Code or Cursor on another machine, forward its web apps to `localhost`, and watch its CPU, memory and GPU — without setting up SSH keys, VPNs or port forwarding by hand.

Think of it as NVIDIA Sync for every computer you own.

> **Status:** Phase 1 (device access) — early preview. macOS builds first; Windows and Linux are next.
> Phase 2 adds browsing other devices' files directly in Finder / File Explorer.

## How it works

1. **Install** TailConnect on two or more computers.
2. **Sign in** once on each with the same account (Google, Microsoft, GitHub, Apple… via Tailscale; free).
3. Your devices **find each other and pair automatically.** That's it.

There's no Tailscale app to install — TailConnect embeds its own Tailscale node (it shows up as `tc-<name>` in your Tailscale admin console) and works alongside the Tailscale app if you already have it.

## Features

| | |
|---|---|
| **Automatic pairing** | Devices on the same Tailscale account trust each other with zero prompts. Devices from other accounts send a pairing request you approve or decline. |
| **One-click apps** | Terminal (SSH), VS Code, VS Code Insiders, Cursor, Windsurf and Zed open a remote session on the device. Choose which appear in Settings. |
| **Custom apps & ports** | Forward any port from a device to `localhost`, optionally starting the service with a bash launch script, opening the browser, or running it in a terminal. Local port collisions are resolved automatically. |
| **Resource monitor** | Live CPU (per core), memory, swap, NVIDIA GPU (utilisation, VRAM, temperature, power), disks, network and uptime. |
| **Any SSH host** | Add Linux/macOS machines that don't run TailConnect — on your LAN, the internet or your tailnet. Your password is used once to install a key and is never stored. |
| **Works with your tools** | Every device gets an SSH alias (`ssh tc-desktop`) in `~/.ssh/config`, so `ssh`, `scp`, `rsync`, VS Code and JetBrains just work. |
| **Connection status** | See whether traffic flows directly (LAN / peer-to-peer) or via a relay, with automatic reconnects. |
| **Access control** | See and revoke which devices can access this computer. Removing a device unpairs both sides. |
| **Self-hosting** | Point it at a [Headscale](https://headscale.net) server instead of Tailscale's. |
| **Session log** | Connects, launches, pairings and app starts are logged locally. |

## Install

### macOS (13 Ventura or later, Apple Silicon or Intel)

Download `TailConnect-<version>-macos.dmg` from [Releases](https://github.com/imbikramsaha/TailConnect/releases), open it and drag **TailConnect** to **Applications**, then open it from Applications. It appears in the menu bar.

Preview builds are not notarized yet: the first time, right-click TailConnect → **Open** → **Open**.

To let other devices open terminals on your Mac, turn on **System Settings → General → Sharing → Remote Login**.

Optional command-line tool:

```sh
sudo ln -sf /Applications/TailConnect.app/Contents/MacOS/tailconnect /usr/local/bin/tailconnect
```

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
- Only paired devices can read stats, open port tunnels or SSH in. Pairing installs the other device's key in `~/.ssh/authorized_keys`, tagged `tailconnect:<node-id>`, and revoking removes it.
- Port tunnels only reach `localhost` on the paired device.
- The local control API listens on `127.0.0.1` only, requires a per-run random token, rejects foreign `Host` headers (DNS rebinding) and requires a custom header on every change (CSRF).
- TailConnect never stores SSH passwords.

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
make macos         # dist/TailConnect.app + dist/TailConnect-<version>-macos.dmg (universal)
```

Set `SIGN_IDENTITY` (and `NOTARY_PROFILE`) to sign and notarize for distribution.

## Architecture

```
cmd/tailconnect        app entry point + CLI
internal/core          engine: discovery, pairing, connections, custom apps, stats
internal/tailnet       embedded Tailscale node (tsnet)
internal/agent         device-to-device API on the tailnet (:4747): hello, pair, stats, tunnel
internal/ui            local control API + embedded web UI (127.0.0.1)
internal/tray          menu bar / system tray
internal/sshx, sshkeys, sshconf   SSH client, keys/trust, ~/.ssh/config integration
internal/launch        terminals, editors, browser; internal/sysinfo resource monitor
```

## Roadmap

- **Phase 1** — device access (this release): macOS ✅ · Windows · Linux
- **Phase 2** — other devices' folders in Finder / File Explorer / Files
- Later — auto-update, signed installers, mobile

TailConnect is an independent project and is not affiliated with Tailscale Inc. or NVIDIA.
