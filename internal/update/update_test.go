package update

import "testing"

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true}, {"v1.0.0", "0.9.9", true}, {"1.0.0", "1.0.0-rc1", true},
		{"1.0.0-rc2", "1.0.0-rc1", true}, {"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false},
		{"0.2.0", "9330d16-dirty", false}, {"0.1.10", "0.1.9", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q) = %v", c.a, c.b, got)
		}
	}
	if Valid("0.1.0-dev") || Valid("9330d16") || !Valid("0.2.0") {
		t.Error("Valid")
	}
	if app, ok := AppBundle("/Applications/TailConnect.app/Contents/MacOS/tailconnect"); !ok || app != "/Applications/TailConnect.app" {
		t.Errorf("AppBundle = %q %v", app, ok)
	}
	if s, ok := expectedSum("abc123  TailConnect-0.2.0-macos.dmg\nffff *other\n", "TailConnect-0.2.0-macos.dmg"); !ok || s != "abc123" {
		t.Errorf("expectedSum %q %v", s, ok)
	}
}
