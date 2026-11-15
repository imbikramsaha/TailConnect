package notify

import "testing"

func TestClean(t *testing.T) {
	if got := clean("a\nb‮c\x07"); got != "a bc" {
		t.Errorf("clean = %q", got)
	}
}
