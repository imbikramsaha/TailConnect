package agent

import (
	"crypto/rand"
	"crypto/sha256"
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

	// hdrRespSig signs a Hello answer with the answering device's key,
	// bound to the request's nonce. It lets a peer tell TailConnect from
	// another program or OS user answering on the same machine's ports
	// (possible with the Tailscale app while TailConnect isn't running).
	hdrRespSig = "X-TC-Resp-Sig"
)

func responseBytes(nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("tailconnect-resp-v1\n" + nonce + "\n" + hex.EncodeToString(sum[:]) + "\n")
}

// signResponse signs body as the answer to the request carrying nonce.
func signResponse(signer ssh.Signer, nonce string, body []byte) (string, error) {
	sig, err := signer.Sign(rand.Reader, responseBytes(nonce, body))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ssh.Marshal(sig)), nil
}

// verifyResponse checks a response signature against key (authorized_keys).
func verifyResponse(key, nonce string, body []byte, sigB64 string) bool {
	if key == "" || sigB64 == "" || len(nonce) != 32 {
		return false
	}
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	var sig ssh.Signature
	if ssh.Unmarshal(raw, &sig) != nil {
		return false
	}
	return pk.Verify(responseBytes(nonce, body), &sig) == nil
}

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

// nonceCache rejects replayed signatures. A nonce only needs remembering
// while its timestamp is acceptable (2 x maxSkew); older entries are pruned
// at most every 30 seconds, and the cache is capped — past the cap new
// requests are refused rather than old nonces forgotten, which would allow
// replays. Per-device rate limits keep honest traffic far below it.
type nonceCache struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	pruned time.Time
}

const maxNonces = 200_000

func (n *nonceCache) fresh(nonce string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]time.Time{}
	}
	now := time.Now()
	if now.Sub(n.pruned) > 30*time.Second {
		for k, t := range n.seen {
			if now.Sub(t) > 2*maxSkew {
				delete(n.seen, k)
			}
		}
		n.pruned = now
	}
	if _, dup := n.seen[nonce]; dup || len(n.seen) >= maxNonces {
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
