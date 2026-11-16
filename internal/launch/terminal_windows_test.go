package launch

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

func TestPSEncodedQuotesEveryArgument(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(psEncoded([]string{"ssh", "-t", "tc-box", `echo it's; rm -rf / & calc "x" $env:PATH`}))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	got := string(utf16.Decode(u))
	want := `& 'ssh' '-t' 'tc-box' 'echo it''s; rm -rf / & calc "x" $env:PATH'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
