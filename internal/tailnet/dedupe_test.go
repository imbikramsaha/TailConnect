package tailnet

import (
	"fmt"
	"testing"
)

func TestDedupe(t *testing.T) {
	var got []string
	logf := dedupe(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	for _, m := range []string{"go to: A", "go to: A", "go to: A", "state Starting", "go to: B", "go to: B"} {
		logf("%s", m)
	}
	logf("100%% done %d", 1)
	want := []string{"go to: A", "state Starting", "go to: B", "100% done 1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
