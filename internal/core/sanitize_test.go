package core

import "testing"

func TestSanitizers(t *testing.T) {
	for _, bad := range []string{"", "a\nb", "x\rProxyCommand", "%d", "-oFoo", `a"b`, "a#b", " lead", "a`b", "toolongtoolongtoolongtoolongtoolongtoolongtoolongtoolongtoolong12"} {
		if _, ok := SafeUser(bad); ok {
			t.Errorf("SafeUser accepted %q", bad)
		}
	}
	for _, good := range []string{"bikram", "John Smith", "user.name", "first-last", "DOMAIN-user_1"} {
		if _, ok := SafeUser(good); !ok {
			t.Errorf("SafeUser rejected %q", good)
		}
	}
	for _, bad := range []string{"relative", "/a\nb", `C:x`, "/x%d"} {
		if _, ok := SafeHome(bad); ok {
			t.Errorf("SafeHome accepted %q", bad)
		}
	}
	for _, good := range []string{"/home/u", "/Users/Bikram Saha", `C:\Users\John Smith`} {
		if _, ok := SafeHome(good); !ok {
			t.Errorf("SafeHome rejected %q", good)
		}
	}
	if SafeOS("darwin") != "darwin" || SafeOS("<img>") != "" {
		t.Error("SafeOS")
	}
	if n := SafeName("evil\u202enaem\x07\n"); n != "evilnaem" {
		t.Errorf("SafeName = %q", n)
	}
}
