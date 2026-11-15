package agent

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Every request from TailConnect is signed with the device's TailConnect
// SSH key. Tailnet identity (WhoIs) says which machine is calling; the
// signature proves it's TailConnect on that machine and not another process
// or OS user sharing its Tailscale connection (Tailscale app in TUN mode,
// containers, other accounts). Trusted callers must sign with the key they
// paired with.
const (
	hdrTime  = "X-TC-Time"
	hdrNonce = "X-TC-Nonce"
	hdrSig   = "X-TC-Sig"
	maxSkew  = 5 * time.Minute
)

func signedBytes(method, uri, host, ts, nonce string) []byte {
	return []byte("tailconnect-req-v2\n" + method + "\n" + uri + "\n" + host + "\n" + ts + "\n" + nonce + "\n")
}

// signRequest adds signature headers to req.
func signRequest(req *http.Request, signer ssh.Signer) error {
	if signer == nil {
		return nil
	}
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := hex.EncodeToString(nb)
	sig, err := signer.Sign(rand.Reader, signedBytes(req.Method, req.URL.RequestURI(), req.URL.Host, ts, nonce))
	if err != nil {
		return err
	}
	req.Header.Set(hdrTime, ts)
	req.Header.Set(hdrNonce, nonce)
	req.Header.Set(hdrSig, base64.StdEncoding.EncodeToString(ssh.Marshal(sig)))
	return nil
}

// nonceCache rejects replayed signatures.
type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (n *nonceCache) fresh(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]time.Time{}
	}
	now := time.Now()
	if len(n.seen) > 4096 {
		for k, t := range n.seen {
			if now.Sub(t) > 2*maxSkew {
				delete(n.seen, k)
			}
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return false
	}
	n.seen[nonce] = now
	return true
}

var errUnsigned = errors.New("request is not signed")

// verifyRequest checks r's signature against key (authorized_keys format).
func (n *nonceCache) verifyRequest(r *http.Request, key string) error {
	sigB64, ts, nonce := r.Header.Get(hdrSig), r.Header.Get(hdrTime), r.Header.Get(hdrNonce)
	if sigB64 == "" {
		return errUnsigned
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return errors.New("no key on file for this device")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(nonce) != 32 {
		return errors.New("malformed signature")
	}
	if d := time.Since(time.Unix(sec, 0)); d > maxSkew || d < -maxSkew {
		return errors.New("request time is off by more than 5 minutes; check the clocks")
	}
	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errors.New("malformed signature")
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(raw, &sig); err != nil {
		return errors.New("malformed signature")
	}
	if err := pk.Verify(signedBytes(r.Method, r.URL.RequestURI(), r.Host, ts, nonce), &sig); err != nil {
		return errors.New("signature does not match this device's paired key")
	}
	if !n.fresh(nonce) {
		return errors.New("replayed request")
	}
	return nil
}
