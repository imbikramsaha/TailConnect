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
	dst := "/Applications/RustDesk.app"
	if out, err := exec.CommandContext(ctx, "ditto", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("copying RustDesk: %s", strings.TrimSpace(string(out)))
	}
	return verifyApp(ctx, dst)
}

// verifyApp checks RustDesk's signature: intact, RustDesk's Developer ID
// team and bundle, and notarized (Gatekeeper accepts it).
func verifyApp(ctx context.Context, app string) error {
	if out, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", app).CombinedOutput(); err != nil {
		return fmt.Errorf("RustDesk's signature is broken: %s", strings.TrimSpace(string(out)))
	}
	out, _ := exec.CommandContext(ctx, "codesign", "-dv", "--verbose=2", app).CombinedOutput()
	info := string(out)
	if !strings.Contains(info, "TeamIdentifier="+macTeamID+"\n") || !strings.Contains(info, "Identifier="+macBundleID+"\n") {
		return errors.New("RustDesk isn't signed by RustDesk's developer; not installing it")
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

// repairUserConfig gives the user back RustDesk files that an earlier
// administrator run (TailConnect 0.3.9) left owned by root in the user's
// folders: settings the user's RustDesk then can't read (so it loses direct
// access and its password) and log folders. It reports whether it changed
// anything. Only RustDesk's own files are touched, through folder handles,
// never following links (see giveBack): this runs as root in folders the
// user controls.
func repairUserConfig(uid int) (changed bool) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return false
	}
	gid, _ := strconv.Atoi(u.Gid)
	if dfd, err := openDir(u.HomeDir, "Library", "Preferences", "com.carriez.RustDesk"); err == nil {
		changed = giveBack(dfd, uid, gid, func(n string) bool {
			return strings.HasPrefix(n, "RustDesk") && strings.HasSuffix(n, ".toml")
		})
		unix.Close(dfd)
	}
	for _, sub := range []string{"option", "password"} {
		if dfd, err := openDir(u.HomeDir, "Library", "Logs", "RustDesk", sub); err == nil {
			giveBack(dfd, uid, gid, func(n string) bool { return strings.HasSuffix(n, ".log") })
			unix.Close(dfd)
		}
	}
	return changed
}
