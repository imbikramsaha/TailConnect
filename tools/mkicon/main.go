// Command mkicon writes TailConnect's app icon from internal/icon:
//
//	go run ./tools/mkicon <outdir>
//
// It produces a macOS .iconset (icon_*.png), tailconnect.ico (Windows) and
// tailconnect.png (256px, Linux).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"os"
	"path/filepath"

	"github.com/imbikramsaha/tailconnect/internal/icon"
)

func pngBytes(px int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, icon.AppIcon(px)); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ico packs PNG images into a .ico container (supported since Vista).
func ico(sizes []int) []byte {
	var imgs [][]byte
	for _, s := range sizes {
		imgs = append(imgs, pngBytes(s))
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := byte(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		b.Write([]byte{dim, dim, 0, 0})
		_ = binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
		_ = binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(imgs[i])), uint32(offset)})
		offset += len(imgs[i])
	}
	for _, img := range imgs {
		b.Write(img)
	}
	return b.Bytes()
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkicon <outdir>")
		os.Exit(2)
	}
	out := os.Args[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		panic(err)
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o644); err != nil {
			panic(err)
		}
	}
	for _, s := range []int{16, 32, 128, 256, 512} {
		write(fmt.Sprintf("icon_%dx%d.png", s, s), pngBytes(s))
		write(fmt.Sprintf("icon_%dx%d@2x.png", s, s), pngBytes(s*2))
	}
	write("tailconnect.ico", ico([]int{16, 24, 32, 48, 64, 128, 256}))
	write("tailconnect.png", pngBytes(256))
}
