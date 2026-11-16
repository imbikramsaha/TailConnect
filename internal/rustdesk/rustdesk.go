// Package rustdesk installs and configures RustDesk for screen sharing
// through TailConnect.
//
// Downloads come only from RustDesk's GitHub releases and are checked
// against the SHA-256 GitHub records for the file, plus RustDesk's code
// signature on macOS (Developer ID team HZF9JMC8YN, notarized) and Windows
// (Authenticode, PURSLANE). Installing and configuring need administrator
// rights (RustDesk only accepts settings from an administrator), so they
// run in one elevated step: Apply.
package rustdesk

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Port is RustDesk's direct-IP-access port.
const Port = 21118

const (
	macTeamID    = "HZF9JMC8YN"
	macBundleID  = "com.carriez.rustdesk"
	winSignerCN  = "CN=PURSLANE"
	maxDownload  = 300 << 20
	releasesAPI  = "https://api.github.com/repos/rustdesk/rustdesk/releases/latest"
	downloadHost = "github.com"
)

// Spec is what the elevated step does. It's written by the user's process
// and read by the elevated one; it contains no paths or commands, only
// choices, so it can't make the elevated step run anything else.
type Spec struct {
	Password string `json:"password"` // permanent password to set ("" = leave)
	Lock     bool   `json:"lock"`     // accept connections only through TailConnect
	UID      int    `json:"uid"`      // macOS: user whose session should run RustDesk
}

// Result is what the elevated step reports back.
type Result struct {
	Version     string `json:"version,omitempty"`
	Installed   bool   `json:"installed"`   // RustDesk was installed by this run
	PasswordSet bool   `json:"passwordSet"` // RustDesk accepted the password
	Error       string `json:"error,omitempty"`
}

// NewPassword returns a random permanent password (letters and digits, so
// it survives every command line and RustDesk's own rules).
func NewPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 24)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		b[i] = alphabet[n.Int64()]
	}
	return string(b)
}

// Bin is the installed RustDesk binary, from fixed install locations only
// (the elevated step must never run something from PATH).
func Bin() string {
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{"/Applications/RustDesk.app/Contents/MacOS/RustDesk"}
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")} {
			if root != "" {
				cands = append(cands, filepath.Join(root, "RustDesk", "rustdesk.exe"))
			}
		}
	default:
		cands = []string{"/usr/bin/rustdesk", "/usr/lib/rustdesk/rustdesk"}
	}
	for _, p := range cands {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// asset is a release file to install.
type asset struct {
	Version, Name, URL, SHA256 string
}

// errRateLimited: GitHub's API refused an anonymous request (60 an hour
// per network address; shared offices and CI hit it).
var errRateLimited = errors.New("GitHub is limiting requests from this network right now; try again in an hour")

// latest finds the RustDesk release file for this OS and CPU.
func latest(ctx context.Context) (asset, error) {
	a, err := latestFromAPI(ctx)
	if !errors.Is(err, errRateLimited) || runtime.GOOS == "linux" {
		return a, err
	}
	// macOS/Windows: the download's code signature is verified before it's
	// used, so the version can come from the release page instead (not
	// rate-limited). Linux has only the API's checksum: wait instead.
	v, err := latestVersionFromPage(ctx)
	if err != nil {
		return asset{}, errRateLimited
	}
	name, err := assetName(v)
	if err != nil {
		return asset{}, err
	}
	return asset{Version: v, Name: name, URL: "https://github.com/rustdesk/rustdesk/releases/download/" + v + "/" + name}, nil
}

func latestFromAPI(ctx context.Context) (asset, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "TailConnect")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" { // CI and power users
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return asset{}, errRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return asset{}, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return asset{}, err
	}
	want, err := assetName(rel.Tag)
	if err != nil {
		return asset{}, err
	}
	for _, a := range rel.Assets {
		if a.Name != want {
			continue
		}
		sum, ok := strings.CutPrefix(a.Digest, "sha256:")
		if !ok || len(sum) != 64 {
			return asset{}, errors.New("GitHub didn't record a checksum for " + want + "; not installing it")
		}
		return asset{Version: rel.Tag, Name: a.Name, URL: a.URL, SHA256: strings.ToLower(sum)}, nil
	}
	return asset{}, fmt.Errorf("RustDesk %s has no %s download", rel.Tag, want)
}

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// latestVersionFromPage reads the latest version from where GitHub's
// "latest release" page redirects (/releases/tag/<version>).
func latestVersionFromPage(ctx context.Context) (string, error) {
	cl := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com/rustdesk/rustdesk/releases/latest", nil)
	req.Header.Set("User-Agent", "TailConnect")
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	_, v, ok := strings.Cut(loc, "/releases/tag/")
	if !ok || !versionRe.MatchString(v) {
		return "", errors.New("couldn't find RustDesk's latest version")
	}
	return v, nil
}

// assetName is the release file for this platform.
func assetName(v string) (string, error) {
	if strings.ContainsAny(v, "/\\ ") || v == "" {
		return "", errors.New("unexpected RustDesk version " + v)
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		return "", errors.New("RustDesk isn't available for " + runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("rustdesk-%s-%s.dmg", v, arch), nil
	case "windows":
		return fmt.Sprintf("rustdesk-%s-%s.exe", v, arch), nil
	case "linux":
		if _, err := exec.LookPath("dpkg"); err == nil {
			return fmt.Sprintf("rustdesk-%s-%s.deb", v, arch), nil
		}
		if _, err := exec.LookPath("zypper"); err == nil {
			return fmt.Sprintf("rustdesk-%s-0.%s-suse.rpm", v, arch), nil
		}
		if _, err := exec.LookPath("rpm"); err == nil {
			return fmt.Sprintf("rustdesk-%s-0.%s.rpm", v, arch), nil
		}
		return "", errors.New("install RustDesk from rustdesk.com (no .deb or .rpm support on this Linux)")
	}
	return "", errors.New("RustDesk isn't supported on " + runtime.GOOS)
}

var httpClient = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		h := req.URL.Hostname()
		if req.URL.Scheme != "https" || (h != downloadHost && !strings.HasSuffix(h, ".githubusercontent.com")) {
			return fmt.Errorf("refusing to follow a redirect to %s", h)
		}
		return nil
	},
}

// download fetches a into dir and checks its SHA-256.
func download(ctx context.Context, a asset, dir string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if req.URL.Scheme != "https" || req.URL.Hostname() != downloadHost {
		return "", errors.New("unexpected download location")
	}
	req.Header.Set("User-Agent", "TailConnect")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	p := filepath.Join(dir, filepath.Base(a.Name))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxDownload {
		return "", errors.New("download is unexpectedly large")
	}
	// No recorded checksum only happens on macOS/Windows (see latest), where
	// install verifies RustDesk's code signature before using the file.
	if a.SHA256 == "" && runtime.GOOS == "linux" {
		return "", errors.New("no checksum to verify RustDesk against; not installing it")
	}
	if got := hex.EncodeToString(h.Sum(nil)); a.SHA256 != "" && got != a.SHA256 {
		return "", fmt.Errorf("checksum mismatch for %s; not installing it", a.Name)
	}
	return p, nil
}

// Apply installs RustDesk if needed and configures it for TailConnect. It
// must run with administrator rights. ready is called once RustDesk is
// installed, before configuring (macOS: start it in the user's session).
func Apply(ctx context.Context, spec Spec) Result {
	var res Result
	if Bin() == "" || verifyInstalled(ctx) != nil {
		a, err := latest(ctx)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		dir, err := os.MkdirTemp("", "tailconnect-rustdesk-")
		if err != nil {
			res.Error = err.Error()
			return res
		}
		defer os.RemoveAll(dir)
		p, err := download(ctx, a, dir)
		if err == nil {
			err = install(ctx, p)
		}
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Installed, res.Version = true, a.Version
	}
	bin := Bin()
	if bin == "" {
		res.Error = "RustDesk didn't install correctly"
		return res
	}
	if res.Version == "" {
		res.Version = installedVersion(bin)
	}
	if runtime.GOOS == "darwin" && spec.UID > 0 {
		if repairUserConfig(spec.UID, true) {
			// A running RustDesk read its settings before the repair and
			// would write over it: stop it first, then start it afresh.
			stopInSession(spec.UID)
			repairUserConfig(spec.UID, false)
		}
		startInSession(spec.UID)
	}
	// RustDesk's command line sends settings to the running RustDesk (which
	// saves them as the user) and then also writes its own copy under $HOME.
	// Run as an administrator with the user's HOME (macOS keeps it), that
	// copy would be a root-owned file in the user's settings folder, which
	// the user's RustDesk then can't read. Give it a private HOME instead.
	home, err := os.MkdirTemp("", "tailconnect-rustdesk-home-")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer os.RemoveAll(home)
	run := func(ctx context.Context, bin string, args ...string) (string, error) {
		return runIn(ctx, home, bin, args...)
	}
	var errs []string
	set := func(k, v string) {
		if out, err := run(ctx, bin, "--option", k, v); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v %s", k, err, out))
		}
	}
	set("direct-server", "Y")
	if spec.Lock {
		set("whitelist", "127.0.0.1,::1")
	} else {
		set("whitelist", "")
	}
	set("approve-mode", "password")
	if spec.Password != "" {
		out, err := run(ctx, bin, "--password", spec.Password)
		res.PasswordSet = err == nil && strings.Contains(out, "Done")
		// Only switch to "permanent password only" once RustDesk has one,
		// or nobody could connect at all.
		if res.PasswordSet {
			set("verification-method", "use-permanent-password")
		}
	}
	if len(errs) > 0 {
		res.Error = "some RustDesk settings weren't applied: " + strings.Join(errs, "; ")
	}
	return res
}

// runIn runs RustDesk's command line (fixed binary, fixed option names)
// with HOME set to home (unless empty).
func runIn(ctx context.Context, home, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if home != "" && runtime.GOOS != "windows" {
		env := []string{"HOME=" + home}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "HOME=") {
				env = append(env, kv)
			}
		}
		cmd.Env = env
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func installedVersion(bin string) string {
	out, err := runIn(context.Background(), "", bin, "--version")
	if err != nil || len(out) > 40 || strings.ContainsAny(out, " \n") {
		return ""
	}
	return out
}
