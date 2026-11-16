package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/imbikramsaha/tailconnect/internal/netx"
	"github.com/imbikramsaha/tailconnect/internal/sysinfo"
)

// DialFunc dials over the tailnet.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Client talks to other devices' agent APIs. Every request is signed with
// signer (this device's TailConnect key).
type Client struct {
	dial   DialFunc
	hc     *http.Client
	signer ssh.Signer
}

// NewClient returns a client dialling through dial and signing with signer.
func NewClient(dial DialFunc, signer ssh.Signer) *Client {
	tr := &http.Transport{
		DialContext:           dial,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		// A malicious peer must not exhaust our memory with its answers.
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &Client{dial: dial, hc: &http.Client{Transport: tr}, signer: signer}
}

func base(ip string) string {
	return "http://" + net.JoinHostPort(ip, strconv.Itoa(Port))
}

func (c *Client) do(ctx context.Context, method, ip, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base(ip)+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := signRequest(req, c.signer); err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &StatusError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// StatusError is a non-200 agent response.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string { return fmt.Sprintf("agent: %d %s", e.Code, e.Msg) }

// Proof reports whether a response was signed, for the request that
// fetched it, by key (authorized_keys format).
type Proof func(key string) bool

// Hello fetches a device's description, with a Proof of who answered.
func (c *Client) Hello(ctx context.Context, ip string) (*Hello, Proof, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base(ip)+"/v1/hello", nil)
	if err != nil {
		return nil, nil, err
	}
	if err := signRequest(req, c.signer); err != nil {
		return nil, nil, err
	}
	nonce := req.Header.Get(hdrNonce)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, nil, &StatusError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var h Hello
	if err := json.Unmarshal(body, &h); err != nil {
		return nil, nil, err
	}
	if h.App != AppID {
		return nil, nil, fmt.Errorf("not a TailConnect device")
	}
	sig := resp.Header.Get(hdrRespSig)
	return &h, func(key string) bool { return verifyResponse(key, nonce, body, sig) }, nil
}

// Pair asks ip to trust us.
func (c *Client) Pair(ctx context.Context, ip string, req PairRequest) (*PairResponse, error) {
	var r PairResponse
	return &r, c.do(ctx, http.MethodPost, ip, "/v1/pair", req, &r)
}

// PairStatus polls a pending pairing request.
func (c *Client) PairStatus(ctx context.Context, ip, id string) (*PairResponse, error) {
	var r PairResponse
	return &r, c.do(ctx, http.MethodGet, ip, "/v1/pair/"+url.PathEscape(id), nil, &r)
}

// Unpair asks ip to forget us.
func (c *Client) Unpair(ctx context.Context, ip string) error {
	return c.do(ctx, http.MethodPost, ip, "/v1/unpair", struct{}{}, nil)
}

// Stats fetches a resource snapshot.
func (c *Client) Stats(ctx context.Context, ip string) (*sysinfo.Stats, error) {
	var s sysinfo.Stats
	return &s, c.do(ctx, http.MethodGet, ip, "/v1/stats", nil, &s)
}

// Tunnel opens a raw TCP stream to a loopback port on ip.
func (c *Client) Tunnel(ctx context.Context, ip string, port int) (net.Conn, error) {
	conn, err := c.dial(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(Port)))
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	}
	req, _ := http.NewRequest(http.MethodGet, base(ip)+"/v1/tunnel?port="+strconv.Itoa(port), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnelProto)
	if err := signRequest(req, c.signer); err != nil {
		conn.Close()
		return nil, err
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	// Cap the response header a peer can make us buffer; lifted once the
	// tunnel is established.
	lr := &io.LimitedReader{R: conn, N: 64 << 10}
	br := bufio.NewReader(lr)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		conn.Close()
		return nil, &StatusError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	_ = conn.SetDeadline(time.Time{})
	lr.N = math.MaxInt64
	return &netx.BufConn{Conn: conn, R: br}, nil
}

// Services lists services listening on the device.
func (c *Client) Services(ctx context.Context, ip string) ([]Service, error) {
	var s []Service
	err := c.do(ctx, http.MethodGet, ip, "/v1/services", nil, &s)
	return s, err
}

// Processes lists the device's busiest processes.
func (c *Client) Processes(ctx context.Context, ip string) ([]Process, error) {
	var p []Process
	err := c.do(ctx, http.MethodGet, ip, "/v1/processes", nil, &p)
	return p, err
}

// Kill stops a process on the device.
func (c *Client) Kill(ctx context.Context, ip string, pid int) error {
	return c.do(ctx, http.MethodPost, ip, "/v1/processes/"+strconv.Itoa(pid)+"/kill", struct{}{}, nil)
}

// Containers lists Docker containers on the device.
func (c *Client) Containers(ctx context.Context, ip string) ([]Container, error) {
	var cs []Container
	err := c.do(ctx, http.MethodGet, ip, "/v1/containers", nil, &cs)
	return cs, err
}

// ContainerAction starts/stops/restarts a container on the device.
func (c *Client) ContainerAction(ctx context.Context, ip, id, action string) error {
	return c.do(ctx, http.MethodPost, ip, "/v1/containers/"+url.PathEscape(id)+"/"+url.PathEscape(action), struct{}{}, nil)
}

// Wake asks the device to send Wake-on-LAN packets on its network.
func (c *Client) Wake(ctx context.Context, ip string, req WakeRequest) error {
	return c.do(ctx, http.MethodPost, ip, "/v1/wake", req, nil)
}

// Speed downloads n bytes from the device and returns the throughput.
func (c *Client) Speed(ctx context.Context, ip string, n int64) (bytesPerSec float64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base(ip)+"/v1/speed?bytes="+strconv.FormatInt(n, 10), nil)
	if err != nil {
		return 0, err
	}
	if err := signRequest(req, c.signer); err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, &StatusError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	got, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0, err
	}
	return float64(got) / time.Since(start).Seconds(), nil
}

// SendFile uploads r as name to the device's inbox.
func (c *Client) SendFile(ctx context.Context, ip, name string, size int64, r io.Reader) (*FileResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base(ip)+"/v1/files?name="+url.QueryEscape(name), r)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := signRequest(req, c.signer); err != nil {
		return nil, err
	}
	hc := &http.Client{Transport: c.hc.Transport} // no overall timeout for big files
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &StatusError{Code: resp.StatusCode, Msg: strings.TrimSpace(string(msg))}
	}
	var fr FileResult
	return &fr, json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&fr)
}

// GetClipboard reads the device's clipboard.
func (c *Client) GetClipboard(ctx context.Context, ip string) (string, error) {
	var cb Clipboard
	err := c.do(ctx, http.MethodGet, ip, "/v1/clipboard", nil, &cb)
	return cb.Text, err
}

// SetClipboard writes the device's clipboard.
func (c *Client) SetClipboard(ctx context.Context, ip, text string) error {
	return c.do(ctx, http.MethodPut, ip, "/v1/clipboard", Clipboard{Text: text}, nil)
}
