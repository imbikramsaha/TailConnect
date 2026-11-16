package rustdesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Never follow links: this runs as an administrator on a file in the
	// user's temporary folder.
	fi, err := os.Lstat(specPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4096 {
		return errors.New("bad spec file")
	}
	f, err := openNoFollow(specPath)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	f.Close()
	_ = os.Remove(specPath) // it holds the password
	if err != nil {
		return err
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return errors.New("bad spec file")
	}
	if spec.Password != "" && !passwordRe.MatchString(spec.Password) {
		return errors.New("bad spec file")
	}
	if spec.UID < 0 {
		return errors.New("bad spec file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	res := Apply(ctx, spec)
	out, _ := json.Marshal(res)
	// O_EXCL: never write through something planted at the result path.
	rf, err := os.OpenFile(specPath+".result", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("writing result: %w", err)
	}
	_, err = rf.Write(out)
	if cerr := rf.Close(); err == nil {
		err = cerr
	}
	return err
}
