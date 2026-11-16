package rustdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var passwordRe = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

// Setup asks for administrator rights and runs the elevated step (exe with
// the "rustdesk-admin" command), returning its result.
func Setup(ctx context.Context, exe string, spec Spec) (Result, error) {
	if spec.Password != "" && !passwordRe.MatchString(spec.Password) {
		return Result{}, errors.New("invalid RustDesk password")
	}
	dir, err := os.MkdirTemp("", "tailconnect-screen-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	specPath := filepath.Join(dir, "spec.json")
	b, _ := json.Marshal(spec)
	if err := os.WriteFile(specPath, b, 0o600); err != nil {
		return Result{}, err
	}
	if err := elevate(ctx, exe, specPath); err != nil {
		return Result{}, err
	}
	rb, err := os.ReadFile(specPath + ".result")
	if err != nil {
		return Result{}, errors.New("setup didn't finish (was the administrator prompt cancelled?)")
	}
	var res Result
	if err := json.Unmarshal(rb, &res); err != nil {
		return Result{}, err
	}
	return res, nil
}

// AdminMain is the elevated step ("tailconnect rustdesk-admin <spec>").
func AdminMain(specPath string) error {
	if !filepath.IsAbs(specPath) {
		return errors.New("spec path must be absolute")
	}
	// This runs as an administrator on a file in the user's temporary
	// folder: work through a handle on that folder, never following links.
	dir, err := openSpecDir(filepath.Dir(specPath))
	if err != nil {
		return errors.New("bad spec file")
	}
	defer dir.close()
	name := filepath.Base(specPath)
	b, owner, err := dir.read(name, 4096) // removed after reading: it holds the password
	if err != nil {
		return errors.New("bad spec file")
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return errors.New("bad spec file")
	}
	if spec.Password != "" && !passwordRe.MatchString(spec.Password) {
		return errors.New("bad spec file")
	}
	// The user whose RustDesk settings get repaired (macOS) is the one who
	// wrote the spec — not whatever user ID the file claims.
	if spec.UID < 0 || spec.UID > 0 && spec.UID != owner {
		return errors.New("bad spec file")
	}
	useSystemTemp()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res := Apply(ctx, spec)
	out, _ := json.Marshal(res)
	if err := dir.create(name+".result", out); err != nil {
		return fmt.Errorf("writing result: %w", err)
	}
	return nil
}
