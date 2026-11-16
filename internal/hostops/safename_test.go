package hostops

import (
	"strings"
	"testing"
)

func TestSafeNameWindowsDeviceNames(t *testing.T) {
	for _, in := range []string{"CON", "con.txt", "CON .txt", "nul", "COM1.log", "COM¹", "lpt³.txt", "CONIN$", "conout$.dat", "AUX.tar.gz"} {
		got, err := SafeName(in)
		if err != nil || !strings.HasPrefix(got, "_") {
			t.Errorf("SafeName(%q) = %q, %v; want a prefixed name", in, got, err)
		}
	}
	for _, in := range []string{"console.txt", "COM10", "nullify.go", "report.pdf"} {
		if got, _ := SafeName(in); got != in {
			t.Errorf("SafeName(%q) = %q; ordinary name changed", in, got)
		}
	}
}
