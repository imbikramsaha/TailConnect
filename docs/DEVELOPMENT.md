# Development

This guide covers building TailConnect from source, running it locally, the project layout and how releases are made. For using TailConnect, see the [README](../README.md).

## Requirements

- **Go 1.27** or later
- **macOS:** Xcode command-line tools (`xcode-select --install`)
- **Linux:** `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` for the native window and tray
- **Windows:** [Inno Setup 6](https://jrsoftware.org/isinfo.php) to build the installer (optional)

## Build and test

```sh
make test        # unit tests plus two-node end-to-end tests on an in-process tailnet
make test-short  # unit tests only
make vet         # go vet
make run         # build and run TailConnect for this machine
make build       # build into dist/
```

The end-to-end tests start a real Tailscale control server, DERP relay and two TailConnect instances inside the test process, so they need no network access or Tailscale account.

Fuzz tests cover everything that parses input from other devices — request signatures, sanitizers, SSH config rendering, file names, command output parsers and update metadata. Run one with:

```sh
go test ./internal/agent -run '^$' -fuzz FuzzVerifyRequest -fuzztime 30s
```

## Try the UI without two computers

`TestDemo` runs two paired instances ("MacBook Air" and "Mac mini") on an in-process tailnet with live UI servers:

```sh
TC_DEMO=/tmp/tc-demo go test ./internal/ui -run TestDemo -timeout 60m
```

Open the login links written to `/tmp/tc-demo/urls.txt`. Create `/tmp/tc-demo/stop` to shut it down. The README screenshots are taken from this demo.

## Packages

| Platform | Command | Output |
|---|---|---|
| macOS | `make macos` | `dist/TailConnect.app`, a universal `.dmg` and `SHA256SUMS` |
| Windows | `scripts/build-windows.sh <version>` | `TailConnectApp.exe` (GUI), `tailconnect.exe` (CLI), a `.zip` and the Inno Setup installer |
| Linux | `scripts/build-linux.sh <version>` | `.tar.gz`, `.deb` and `.rpm` |
| Homebrew | `scripts/update-cask.sh <version>` | the cask for [imbikramsaha/homebrew-tap](https://github.com/imbikramsaha/homebrew-tap) |

To sign and notarize the macOS build, set `SIGN_IDENTITY`, `NOTARY_PROFILE` and `TEAM_ID`. Builds with a `TEAM_ID` only accept updates signed by the same Apple team.

## Project layout

```text
cmd/tailconnect          App entry point and CLI; `tailconnect window` is the native window process
cmd/tailconnect-window   WebKitGTK window helper for Linux
internal/core            Engine: discovery, pairing and access, connections, apps, services,
                         Wake-on-LAN, diagnostics, alerts, updates
internal/tailnet         Tailscale connection: embedded node (tsnet) or the Tailscale app's local API
internal/agent           Device-to-device API on the tailnet (:4747) with signed requests
internal/sshd            Built-in SSH server on the tailnet (:4748): PTY/ConPTY, exec, SFTP, forwarding
internal/hostops         Local operations: services, processes, Docker, clipboard, files
internal/ui              Local control API and embedded web UI (127.0.0.1)
internal/window          Native window: WKWebView, WebView2, WebKitGTK
internal/tray            Menu bar and system tray
internal/notify          System notifications
internal/update          Signed updates from GitHub Releases
internal/sshx, sshkeys,  SSH client, keys and host trust, ~/.ssh/config integration
  sshconf
internal/launch          Terminals, editors, browser and screen sharing
internal/sysinfo         Resource monitor
internal/store           Settings and paired devices (state.json)
tools/relsign            Release signing key tool
tools/mkicon             App icon generator
packaging/               Inno Setup script, nfpm config, desktop entry, Homebrew cask
scripts/                 Build, release and install scripts
```

## How releases are made

Releases are built by CI and signed offline by the maintainer, so a compromised CI pipeline can't ship an update that apps will install.

1. **Tag.** Push an annotated tag `vX.Y.Z`. Tags are protected by a repository ruleset.
2. **Build.** CI runs the tests, builds every package on its native OS, records [build provenance](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations) for each file and creates a **draft** release.
3. **Sign and publish.** On the maintainer's machine, `scripts/publish-release.sh vX.Y.Z` downloads the draft, checks every file against `SHA256SUMS` and its provenance attestation, signs `SHA256SUMS` with the offline release key (`~/.tailconnect-release/release.key`) and publishes. Releases are immutable once published.
4. **Homebrew.** Copy the release's `tailconnect.rb` to `Casks/tailconnect.rb` in the tap.
5. **Verify.** The *Install test* workflow runs the README's install commands on clean Windows, macOS, Ubuntu (x64 and ARM64) and Fedora machines.

The app only installs an update when `SHA256SUMS.sig` verifies against a public key compiled into it (`internal/update`). The install scripts check the same signature.
