// Package update checks GitHub Releases for new TailConnect versions and,
// on macOS, installs them in place.
package update

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases are published to.
const Repo = "imbikramsaha/TailConnect"

// Release is a published version.
type Release struct {
	Version string            `json:"version"`
	URL     string            `json:"url"`
	Notes   string            `json:"notes"`
	Assets  map[string]string `json:"-"` // name -> download URL
}

// ReleaseKeys are the ed25519 public keys (base64) allowed to sign
// releases. The private key is kept offline by the maintainer; CI cannot
// sign, so a compromised repository or pipeline can't push an update.
var ReleaseKeys = []string{
	"bBlRWaSOYVipG6PdclZi7adlxdc3UxN7JM+LXHjdT6w=",
}

// TeamID, if set at build time (-X .../update.TeamID=ABCDE12345), is the
// Apple developer team updates must be signed by on macOS.
var TeamID = ""

// allowedHosts are the only hosts downloads may come from (GitHub release
// assets redirect to these).
var allowedHosts = map[string]bool{
	"api.github.com": true, "github.com": true, "objects.githubusercontent.com": true,
	"release-assets.githubusercontent.com": true, "github-releases.githubusercontent.com": true,
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" || !allowedHosts[req.URL.Hostname()] {
		return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
	}
	return nil
}

var client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: checkRedirect}

// SignedMessage is what a release signature covers.
func SignedMessage(sums []byte) []byte {
	return append([]byte("tailconnect-release-v1\n"), sums...)
}

// VerifySignature checks a base64 ed25519 signature over sums against any of
// the given base64 public keys.
func VerifySignature(pubs []string, sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("malformed release signature")
	}
	msg := SignedMessage(sums)
	for _, p := range pubs {
		k, err := base64.StdEncoding.DecodeString(p)
		if err != nil || len(k) != ed25519.PublicKeySize {
			continue
		}
		if ed25519.Verify(ed25519.PublicKey(k), msg, raw) {
			return nil
		}
	}
	return errors.New("release signature is not valid")
}

// Latest returns the newest non-draft release (pre-releases included while
// the project is in preview).
func Latest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+Repo+"/releases?per_page=10", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TailConnect-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no releases published yet")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rels []struct {
		Tag     string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		Draft   bool   `json:"draft"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return nil, err
	}
	var best *Release
	for _, r := range rels {
		if r.Draft {
			continue
		}
		if !Valid(r.Tag) || !semverRe.MatchString(r.Tag) || len(r.Tag) > 64 {
			continue // e.g. "nightly": never let a stray tag block updates
		}
		rel := &Release{Version: strings.TrimPrefix(r.Tag, "v"), URL: r.HTMLURL, Notes: r.Body, Assets: map[string]string{}}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		if best == nil || Newer(rel.Version, best.Version) {
			best = rel
		}
	}
	if best == nil {
		return nil, errors.New("no releases published yet")
	}
	return best, nil
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

type ver struct {
	n   [3]int
	pre string
}

func parse(s string) (ver, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ver{}, false
	}
	var v ver
	for i := 0; i < 3; i++ {
		v.n[i], _ = strconv.Atoi(m[i+1])
	}
	v.pre = m[4]
	return v, true
}

// Valid reports whether s is a release version (development builds aren't).
func Valid(s string) bool {
	v, ok := parse(s)
	return ok && !strings.Contains(v.pre, "dev")
}

// Newer reports whether version a is newer than b.
func Newer(a, b string) bool {
	va, ok1 := parse(a)
	vb, ok2 := parse(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true // 1.0.0 > 1.0.0-rc1
	case vb.pre == "":
		return false
	}
	return comparePre(va.pre, vb.pre) > 0
}

// comparePre compares pre-release labels per semver: dot-separated parts,
// numeric parts compared as numbers (rc.10 > rc.9).
func comparePre(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				if na > nb {
					return 1
				}
				return -1
			}
		case ea == nil:
			return -1 // numeric < alphanumeric
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		}
	}
	return len(pa) - len(pb)
}

// AppBundle returns the .app bundle containing exe (macOS).
func AppBundle(exe string) (string, bool) {
	i := strings.Index(exe, ".app/Contents/MacOS/")
	if i < 0 {
		return "", false
	}
	return exe[:i+4], true
}

// CanInstall reports whether in-place installation is supported for exe.
func CanInstall(exe string) bool {
	if runtime.GOOS == "windows" {
		// Installed by our setup program (it leaves an uninstaller beside us).
		_, err := os.Stat(filepath.Join(filepath.Dir(exe), "unins000.exe"))
		return err == nil
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	app, ok := AppBundle(exe)
	if !ok || strings.HasPrefix(app, "/Volumes/") || strings.Contains(app, "/AppTranslocation/") {
		return false
	}
	return writable(filepath.Dir(app))
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".tc-write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// asset returns the release asset with exactly the expected name for this
// version (prevents replaying another release's signed files under a new tag).
func asset(r *Release, suffix string) (name, url string, ok bool) {
	name = "TailConnect-" + r.Version + suffix
	url, ok = r.Assets[name]
	return name, url, ok
}

func macAsset(r *Release) (string, string, bool) { return asset(r, "-macos.dmg") }

// downloadVerified downloads a release asset into dir and checks it against
// the release's SHA256SUMS (required).
func downloadVerified(ctx context.Context, rel *Release, name, url, dir string) (string, error) {
	dst := filepath.Join(dir, name)
	sum, err := download(ctx, url, dst)
	if err != nil {
		return "", err
	}
	su, ok1 := rel.Assets["SHA256SUMS"]
	ss, ok2 := rel.Assets["SHA256SUMS.sig"]
	if !ok1 || !ok2 {
		return "", errors.New("the release isn't signed; download it manually")
	}
	sp := filepath.Join(dir, "SHA256SUMS")
	if _, err := download(ctx, su, sp); err != nil {
		return "", err
	}
	if _, err := download(ctx, ss, sp+".sig"); err != nil {
		return "", err
	}
	b, err := os.ReadFile(sp)
	if err != nil {
		return "", err
	}
	sig, err := os.ReadFile(sp + ".sig")
	if err != nil {
		return "", err
	}
	if err := VerifySignature(ReleaseKeys, b, sig); err != nil {
		return "", fmt.Errorf("refusing update: %w", err)
	}
	want, ok := expectedSum(string(b), name)
	if !ok || want != sum {
		return "", errors.New("downloaded update failed its checksum")
	}
	return dst, nil
}

// Install installs rel over the running app at exe. On success the caller
// should quit: the new version is started for it (macOS relaunch helper,
// Windows installer).
func Install(ctx context.Context, rel *Release, exe string, pid int) error {
	switch runtime.GOOS {
	case "darwin":
		if err := InstallMac(ctx, rel, exe); err != nil {
			return err
		}
		app, _ := AppBundle(exe)
		return Relaunch(app, pid)
	case "windows":
		return installWindows(ctx, rel, exe)
	}
	return errors.New("automatic install isn't supported here; download the update instead")
}

// installWindows runs the release's setup program silently. It closes
// TailConnect, upgrades in place and starts it again.
func installWindows(ctx context.Context, rel *Release, exe string) error {
	if !CanInstall(exe) {
		return errors.New("TailConnect wasn't installed with its setup program; download the update instead")
	}
	name, url, ok := asset(rel, "-windows-setup.exe")
	if !ok {
		return errors.New("the release has no Windows installer")
	}
	dir, err := os.MkdirTemp("", "tailconnect-update-")
	if err != nil {
		return err
	}
	setup, err := downloadVerified(ctx, rel, name, url, dir)
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	// !startup: an update must not turn "start at login" back on.
	cmd := exec.Command(setup, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/CLOSEAPPLICATIONS",
		"/RESTARTAPPLICATIONS", "/MERGETASKS=!startup")
	return cmd.Start()
}

func download(ctx context.Context, url, dst string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "TailConnect-updater")
	resp, err := (&http.Client{Timeout: 15 * time.Minute, CheckRedirect: checkRedirect}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 1<<30))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), err
}

// expectedSum finds name's checksum in a SHA256SUMS file.
func expectedSum(sums, name string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// teamID returns the Apple Team ID an app is signed with ("" = ad-hoc or
// unknown).
func teamID(app string) string {
	out, err := exec.Command("codesign", "-dv", app).CombinedOutput()
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "TeamIdentifier="); ok && v != "not set" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// InstallMac downloads rel's DMG, verifies it and replaces the app that
// contains exe. The caller must then relaunch (see Relaunch).
func InstallMac(ctx context.Context, rel *Release, exe string) error {
	if !CanInstall(exe) {
		return errors.New("automatic install isn't possible from this location; download the update instead")
	}
	cur, _ := AppBundle(exe)
	name, url, ok := macAsset(rel)
	if !ok {
		return errors.New("the release has no macOS download")
	}
	tmp, err := os.MkdirTemp("", "tailconnect-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dmg, err := downloadVerified(ctx, rel, name, url, tmp)
	if err != nil {
		return err
	}
	mnt := filepath.Join(tmp, "mnt")
	if err := attachDMG(ctx, dmg, mnt); err != nil {
		return err
	}
	defer detachDMG(mnt)
	src := filepath.Join(mnt, "TailConnect.app")
	if out, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", src).CombinedOutput(); err != nil {
		return fmt.Errorf("update signature is invalid: %s", strings.TrimSpace(string(out)))
	}
	// Developer-ID builds pin their team: the update must carry the same
	// Apple signature and pass Gatekeeper. (Ad-hoc builds rely on the
	// release signature checked in downloadVerified.)
	want := TeamID
	if want == "" {
		want = teamID(cur)
	}
	if want != "" {
		req := `anchor apple generic and certificate leaf[subject.OU] = "` + want + `"`
		if out, err := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", "-R="+req, src).CombinedOutput(); err != nil {
			return fmt.Errorf("update isn't signed by the expected developer: %s", strings.TrimSpace(string(out)))
		}
		if out, err := exec.CommandContext(ctx, "spctl", "--assess", "--type", "execute", src).CombinedOutput(); err != nil {
			return fmt.Errorf("Gatekeeper rejected the update: %s", strings.TrimSpace(string(out)))
		}
	}
	staged := cur + ".new"
	old := cur + ".old"
	os.RemoveAll(staged)
	os.RemoveAll(old)
	if out, err := exec.CommandContext(ctx, "ditto", src, staged).CombinedOutput(); err != nil {
		return fmt.Errorf("copying update: %s", strings.TrimSpace(string(out)))
	}
	_ = exec.Command("xattr", "-dr", "com.apple.quarantine", staged).Run()
	if err := os.Rename(cur, old); err != nil {
		os.RemoveAll(staged)
		return err
	}
	if err := os.Rename(staged, cur); err != nil {
		_ = os.Rename(old, cur)
		return err
	}
	go os.RemoveAll(old)
	return nil
}

// Relaunch starts app again once process pid has exited.
func Relaunch(app string, pid int) error {
	script := fmt.Sprintf(`while kill -0 %d 2>/dev/null; do sleep 0.2; done; open "$1"`, pid)
	cmd := exec.Command("/bin/sh", "-c", script, "sh", app)
	return cmd.Start()
}

// attachDMG mounts dmg read-only at mnt. macOS 27 deprecates hdiutil attach
// in favour of diskutil image; older systems only have hdiutil.
func attachDMG(ctx context.Context, dmg, mnt string) error {
	if err := os.MkdirAll(mnt, 0o700); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "diskutil", "image", "attach", "--mountOptions", "nobrowse",
		"--readOnly", "--mountPoint", mnt, dmg).CombinedOutput()
	if err == nil {
		return nil
	}
	out2, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mnt, dmg).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mounting update: %s %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(out2)))
	}
	return nil
}

func detachDMG(mnt string) {
	if exec.Command("diskutil", "eject", mnt).Run() != nil {
		_ = exec.Command("hdiutil", "detach", "-force", mnt).Run()
	}
}
