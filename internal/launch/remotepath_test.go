package launch

import "testing"

func TestRemotePathRejectsShellMetacharacters(t *testing.T) {
	for _, home := range []string{
		"/x&calc.exe&", "/home/a|b", "/home/%PATH%", "/home/a^b", "/home/(x)", `/home/"q"`, "/home/a!b", "/home/a\nb", "/home/$(id)",
	} {
		if p := remotePath(home); p != "" {
			t.Errorf("remotePath(%q) = %q, want rejected", home, p)
		}
	}
	for home, want := range map[string]string{
		"/home/alice":        "/home/alice",
		"/Users/Zoë Smith":   "/Users/Zoë Smith",
		`C:\Users\Bob Jones`: "/C:/Users/Bob Jones",
	} {
		if p := remotePath(home); p != want {
			t.Errorf("remotePath(%q) = %q, want %q", home, p, want)
		}
	}
}
