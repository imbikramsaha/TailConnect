// Command mkicon writes TailConnect's app icon as PNGs at the sizes a macOS
// .iconset needs (and a 256px PNG for Linux/Windows), from internal/icon.
//
//	go run ./tools/mkicon <outdir>
package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/imbikramsaha/tailconnect/internal/icon"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkicon <outdir>")
		os.Exit(2)
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	write := func(name string, px int) {
		f, err := os.Create(filepath.Join(out, name))
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := png.Encode(f, icon.AppIcon(px)); err != nil {
			panic(err)
		}
	}
	for _, s := range []int{16, 32, 128, 256, 512} {
		write(fmt.Sprintf("icon_%dx%d.png", s, s), s)
		write(fmt.Sprintf("icon_%dx%d@2x.png", s, s), s*2)
	}
}
