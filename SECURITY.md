# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's **Report a vulnerability** button on the [Security tab](https://github.com/imbikramsaha/TailConnect/security), not in public issues. Include the version, platform and steps to reproduce. You'll get a reply within a few days.

## Trust model

TailConnect gives paired devices access to each other. What a device can do on this computer is exactly the access level you granted it:

| Level | Allows |
|---|---|
| Full access | Terminal and editors (built-in SSH or the system SSH server), files, clipboard, stopping processes, Docker actions — plus everything below. **Equivalent to a shell as your user.** |
| Ports & monitor | Forwarding ports from this computer's localhost (and screen sharing). Any service listening on localhost becomes reachable — limit the ports if some have no password. |
| Monitor only | CPU/memory/GPU/disk stats and the process list (without command lines). |

**Who counts as "your device".** With *Pair automatically with my devices* on (the default), any untagged TailConnect device signed in to the same Tailscale account is paired with the access set in *Access for my devices* (default: full). Tailscale treats that account as one person, and so does TailConnect: anything that can sign in to your Tailscale account, or run as TailConnect on one of your machines, can reach your other machines. You're notified whenever a device is paired automatically. Turn auto-pairing off, or lower its access level, if your account is shared by servers or people you don't fully trust.

**Identity.** A request is accepted only if both hold:

1. Tailscale identifies the caller's machine (`WhoIs`) as a paired device, and
2. the request is signed with the key that device paired with (stored owner-only in `~/.ssh/tailconnect/`).

Other programs or OS users on a paired machine share its Tailscale identity but not its TailConnect key, so they get nothing.

## Updates and releases

- CI builds releases as **drafts**. A maintainer verifies the checksums and signs `SHA256SUMS` with an ed25519 key kept offline (never in CI or the repository) before publishing.
- The app checks that signature against the public key compiled into it, requires assets named for the exact version, only follows redirects to GitHub over HTTPS, and never downgrades. Developer-ID builds additionally require the same Apple team and a Gatekeeper pass.
- GitHub Actions are pinned to commit SHAs, release builds don't use shared caches, and tag names are validated.

## Known limitations

- Preview builds are ad-hoc signed (macOS) and unsigned (Windows); first launch shows an OS warning. Updates are still protected by the release signature.
- Revoking a device ends its sessions and tunnels; on macOS/Linux, background jobs it started with `nohup`/`setsid` keep running (as with OpenSSH).
