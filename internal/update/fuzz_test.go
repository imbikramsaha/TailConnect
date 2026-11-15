package update

import "testing"

func FuzzVersionsAndSignatures(f *testing.F) {
	f.Add("v1.2.3-rc.10", "1.2.3", "sig")
	f.Fuzz(func(t *testing.T, a, b, sig string) {
		_ = Newer(a, b)
		if Newer(a, b) && Newer(b, a) {
			t.Fatalf("both %q > %q and the reverse", a, b)
		}
		_ = Valid(a)
		_, _ = expectedSum(a, b)
		if VerifySignature(ReleaseKeys, []byte(a), []byte(sig)) == nil {
			t.Fatalf("random signature accepted")
		}
	})
}
