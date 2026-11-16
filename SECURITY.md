# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's **Report a vulnerability** button on the [Security tab](https://github.com/imbikramsaha/TailConnect/security), not in public issues. Include the version, platform and steps to reproduce. You'll get a reply within a few days.

## Trust model

TailConnect gives paired devices access to each other. What a device can do on this computer is exactly the access level you granted it:

| Level | Allows |
|---|---|
| Full access | Terminal and editors (built-in SSH or the system SSH server), files, clipboard, stopping processes, Docker actions — plus everything below. **Equivalent to a shell as your user.** |
| Ports & monitor | Forwarding ports from this computer's localhost (and screen sharing). Any service listening on localhost becomes reachable — limit the ports if some have no password. Containers are listed only if they publish a port the device may reach. |
| Monitor only | CPU/memory/GPU/disk stats and the process list (without command lines). |

**Who counts as "your device".** With *Pair automatically with my devices* on (the default), an untagged TailConnect device signed in to the same Tailscale account is paired with the access set in *Access for my devices* (default: full). You're notified each time. Tagged devices and devices of other accounts always need your approval, which defaults to *Monitor only*.

Tailscale identifies machines, not the programs on them: with the Tailscale app (or a system `tailscaled`), every program and OS user on a machine shares its Tailscale identity. TailConnect therefore also requires each device's own key (below). Auto-pairing trusts the **first** TailConnect key a machine of yours presents; after that, a different key always needs your approval. On machines shared with other people, use TailConnect's built-in connection (each OS user gets their own Tailscale node) or turn auto-pairing off.

## How devices authenticate each other

- **Requests.** A request is accepted only if Tailscale identifies the caller's machine (`WhoIs`) as a paired device **and** it is signed with the key that device paired with (stored owner-only in `~/.ssh/tailconnect/`). Signatures cover the method, path, target address, time and a single-use nonce, so a request can't be replayed or redirected to another device.
- **Answers.** Devices sign their answers too. Once a paired device has signed, an answer without its key — for example from another program on that machine while TailConnect isn't running — is refused, and no files, clipboard or connections go to it.
- **Pairing.** Approval requests are bound to the key they were made with; a different key starts a new request (and notification). The 6-digit code includes a fresh random value from the approving device, so it can't be precomputed. Only ed25519 device keys are accepted. A device that unpairs itself while restricted needs approval to come back.
- **Built-in SSH** answers only devices with full access, verified by Tailscale identity *and* paired key, and pins its host key over the authenticated API. It won't run while TailConnect runs as root or an elevated administrator (unless `TAILCONNECT_ALLOW_ELEVATED_SSH=1`), so paired devices never get administrator shells by accident.
- **Limits.** Every device has a request budget (pairing requests far lower), and the number of pending requests and replay records is capped.

## On this computer

- **The app window and CLI** talk to TailConnect on `127.0.0.1` only. There are no cookies: a window receives a single-use ticket, exchanges it for a session (never put in a URL), and sends it as a header. Sessions expire when unused. Requests from websites (other origins, including other ports on 127.0.0.1) are refused, and pages are protected by a strict Content Security Policy. The CLI first verifies it's talking to the real app.
- **Nothing a peer sends is trusted**: names, usernames, home folders, paths and addresses are validated before they reach `~/.ssh/config`, command lines, notifications or the UI.
- **SSH config.** Generated entries turn agent and X11 forwarding off, overriding a `Host *` default, so other machines never get your SSH agent.
- **Received files** go to `Downloads/TailConnect`, never overwrite anything, and are marked as downloaded (macOS quarantine, Windows Mark of the Web).
- **Logs and problem reports** redact sign-in links, auth keys, tokens and passwords. A problem report is only saved locally; review it before sharing.
- **Custom app tokens** (`$TC_APP_TOKEN`) are passed through an owner-only file, never on a command line.
- TailConnect never stores SSH passwords, and shows a new host's fingerprint for you to confirm before sending one.

## Updates and releases

- CI builds releases as **drafts**. A maintainer verifies the checksums and build attestations and signs `SHA256SUMS` with an ed25519 key kept offline (never in CI or the repository), protected by a passphrase, before publishing.
- The app checks that signature against the public key compiled into it, requires assets named for the exact version (so an old release can't be replayed as a new one), only follows redirects to GitHub over HTTPS, and never downgrades. Developer-ID builds additionally require the same Apple team and a Gatekeeper pass. The install scripts check the same signature.
- GitHub Actions are pinned to commit SHAs, release builds don't use shared caches, and tag names are validated. Only maintainers can create release tags, and published releases are immutable.
- Every release file has a signed build-provenance attestation. To check a download came from this repository's CI:

  ```sh
  gh attestation verify TailConnect-<version>-macos.dmg --repo imbikramsaha/TailConnect
  ```

- CodeQL, `govulncheck`, Dependabot and fuzz tests (for everything that parses data from other devices) run continuously.

## Known limitations

- Preview builds are ad-hoc signed (macOS) and unsigned (Windows); first launch shows an OS warning. Updates are still protected by the release signature.
- Web apps you open through TailConnect run at `http://localhost:<port>` in your browser, and browsers share `localhost` cookies across ports (as with `ssh -L` or VS Code port forwarding). Only open web apps from devices you trust.
- Forwarded ports listen on `127.0.0.1`, so other programs and OS users on this computer can reach them while they're open (as with `ssh -L`).
- Revoking a device ends its sessions and tunnels. Processes it deliberately detached (`nohup`/`setsid` on macOS/Linux; scheduled tasks or WMI on Windows) keep running, as with OpenSSH. A device with full access can always leave something behind as your user — revoke and review if you stop trusting one.
- Devices still on TailConnect 0.3.6 or earlier don't sign their answers; their answers are accepted as before until they update.
