package agent

import (
	"net/http/httptest"
	"testing"
)

func FuzzVerifyRequest(f *testing.F) {
	f.Add("AAAA", "1700000000", "00112233445566778899aabbccddeeff", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl")
	f.Fuzz(func(t *testing.T, sig, ts, nonce, key string) {
		r := httptest.NewRequest("GET", "http://100.64.0.1:4747/v1/stats", nil)
		r.Header.Set(hdrSig, sig)
		r.Header.Set(hdrTime, ts)
		r.Header.Set(hdrNonce, nonce)
		var n nonceCache
		if n.verifyRequest(r, key) == nil {
			t.Fatalf("forged request accepted")
		}
	})
}
