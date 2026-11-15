package logx

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestOneLinePerRecord(t *testing.T) {
	var b bytes.Buffer
	l := log.New(oneLine{&b}, "session: ", 0)
	l.Printf("device %s paired", "evil\n2026/01/01 00:00:00 session: approved \"admin\"\r")
	if n := strings.Count(b.String(), "\n"); n != 1 {
		t.Fatalf("record split into %d lines: %q", n, b.String())
	}
}
