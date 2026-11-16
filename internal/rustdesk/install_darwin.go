package rustdesk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func install(ctx context.Context, dmg string) error {
	mnt, err := os.MkdirTemp("", "rustdesk-mnt-")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	if out, err := exec.CommandContext(ctx, "diskutil", "image", "attach", "--mountOptions", "nobrowse", "--readOnly", "--mountPoint", mnt, dmg).CombinedOutput(); err != nil {
		if out2, err2 := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg).CombinedOutput(); err2 != nil {
			return fmt.Errorf("mounting RustDesk: %s %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
		}
	}
	defer func() {
		if exec.Command("diskutil", "eject", mnt).Run() != nil {
			_ = exec.Command("hdiutil", "detach", "-force", mnt).Run()
		}
	}()
	src := filepath.Join(mnt, "RustDesk.app")
	if err := verifyApp(ctx, src); err != nil {
		return err
	}
	// Replace whatever is there (e.g. a copy that failed verification):
	// RemoveAll never follows links.
	if err := os.RemoveAll(macApp); err != nil {
		return fmt.Errorf("replacing RustDesk: %w", err)
	}
	if out, err := exec.CommandContext(ctx, "ditto", src, macApp).CombinedOutput(); err != nil {
		return fmt.Errorf("copying RustDesk: %s", strings.TrimSpace(string(out)))
	}
	if err := ownByRoot(ctx); err != nil {
		_ = os.RemoveAll(macApp)
		return err
	}
	if err := verifyApp(ctx, macApp); err != nil {
		_ = os.RemoveAll(macApp)
		return err
	}
	return nil
}

const macApp = "/Applications/RustDesk.app"

// verifyInstalled checks the installed RustDesk before it runs as root: a
// copy the user put in /Applications is theirs to change. It's made
// root's first (so only an administrator can change it from then on), then
// its signature is checked. A link or anything else fails, and setup
// installs RustDesk afresh. (No Gatekeeper check here: it may need the
// network.)
func verifyInstalled(ctx context.Context) error {
	fi, err := os.Lstat(macApp)
	if err != nil || !fi.IsDir() {
		return errors.New("RustDesk isn't installed as an app")
	}
	if err := ownByRoot(ctx); err != nil {
		return err
	}
	return verifySignature(ctx, macApp)
}

// ownByRoot gives the RustDesk app to root (never following links), like an
// installer package would.
func ownByRoot(ctx context.Context) error {
	if out, err := exec.CommandContext(ctx, "chown", "-R", "-P", "root:wheel", macApp).CombinedOutput(); err != nil {
		return fmt.Errorf("securing RustDesk: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// verifySignature checks the app is intact and signed by RustDesk's
// developer (team and bundle ID).
func verifySignature(ctx context.Context, app string) error {
	if out, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("RustDesk's signature is broken: %s", strings.TrimSpace(string(out)))
	}
	out, _ := exec.CommandContext(ctx, "codesign", "-dv", "--verbose=2", app).CombinedOutput()
	info := string(out)
	if !strings.Contains(info, "TeamIdentifier="+macTeamID+"\n") || !strings.Contains(info, "Identifier="+macBundleID+"\n") {
		return errors.New("RustDesk isn't signed by RustDesk's developer; not using it")
	}
	return nil
}

// verifyApp checks RustDesk's signature: intact, RustDesk's Developer ID
// team and bundle, and notarized (Gatekeeper accepts it).
func verifyApp(ctx context.Context, app string) error {
	if err := verifySignature(ctx, app); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "spctl", "--assess", "--type", "execute", app).CombinedOutput(); err != nil {
		return fmt.Errorf("Gatekeeper rejected RustDesk: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// startInSession starts RustDesk for the user (it listens for direct
// connections while it runs, and asks for the permissions it needs).
func startInSession(uid int) {
	u := strconv.Itoa(uid)
	_ = exec.Command("launchctl", "asuser", u, "sudo", "-u", "#"+u, "open", "-g", "-a", "/Applications/RustDesk.app").Run()
	for i := 0; i < 40; i++ {
		if exec.Command("pgrep", "-x", "-U", u, "RustDesk").Run() == nil {
			time.Sleep(3 * time.Second) // let it open its settings channel
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// stopInSession quits the user's RustDesk (all its processes), waiting up
// to five seconds.
func stopInSession(uid int) {
	u := strconv.Itoa(uid)
	_ = exec.Command("pkill", "-TERM", "-x", "-U", u, "RustDesk").Run()
	for i := 0; i < 20; i++ {
		if exec.Command("pgrep", "-x", "-U", u, "RustDesk").Run() != nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// repairUserConfig fixes RustDesk files that an earlier administrator run
// (TailConnect 0.3.9) left owned by root in the user's folders: settings the
// user's RustDesk then can't read (so it loses direct access and its
// password) and log folders. It reports whether there was anything to fix;
// with dryRun it changes nothing. It works through folder handles, never
// following links (see clearRootFiles): this runs as root in folders the
// user controls.
func repairUserConfig(uid int, dryRun bool) (found bool) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return false
	}
	gid, _ := strconv.Atoi(u.Gid)
	if dfd, err := openDir(u.HomeDir, "Library", "Preferences", "com.carriez.RustDesk"); err == nil {
		found = clearRootFiles(dfd, uid, gid, func(n string) bool {
			return strings.HasPrefix(n, "RustDesk") && strings.HasSuffix(n, ".toml")
		}, dryRun)
		unix.Close(dfd)
	}
	for _, sub := range []string{"option", "password"} {
		if dfd, err := openDir(u.HomeDir, "Library", "Logs", "RustDesk", sub); err == nil {
			if clearRootFiles(dfd, uid, gid, func(n string) bool { return strings.HasSuffix(n, ".log") }, dryRun) {
				found = true
			}
			unix.Close(dfd)
		}
	}
	return found
}
