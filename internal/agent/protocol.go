// Package agent implements the device-to-device API every TailConnect node
// serves on its tailnet address. It is how peers discover each other, pair
// (exchange SSH trust), read resource stats and open port tunnels.
//
// All traffic rides the tailnet (WireGuard-encrypted); callers are
// identified with Tailscale WhoIs, never with anything they claim.
package agent

// Port is the tailnet TCP port of the agent API.
const Port = 4747

// SSHPort is the tailnet TCP port of TailConnect's built-in SSH server.
const SSHPort = 4748

// ProtocolVersion is bumped on incompatible API changes. v2: signed requests.
const ProtocolVersion = 2

// AppID identifies TailConnect in Hello responses.
const AppID = "tailconnect"

const tunnelProto = "tailconnect-tunnel"

// Hello describes a device. Served to any tailnet caller.
type Hello struct {
	App      string `json:"app"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	// User and Home are only revealed to trusted callers.
	User string `json:"user,omitempty"`
	Home string `json:"home,omitempty"`
	// SSH reports whether an SSH server is reachable on the device.
	SSH bool `json:"ssh"`
	// SSHPort is the device's local SSH server port.
	SSHPort int `json:"sshPort,omitempty"`
	// Trusted reports whether the caller is paired with this device.
	Trusted bool `json:"trusted"`
	// Access is the caller's access level on this device ("" if untrusted).
	Access string `json:"access,omitempty"`
	// BuiltinSSH is the built-in SSH server's tailnet port (0 = off). Only
	// revealed to callers with full access.
	BuiltinSSH int `json:"builtinSSH,omitempty"`
	// MACs and LANs let paired devices wake this one (Wake-on-LAN).
	MACs []string `json:"macs,omitempty"`
	LANs []string `json:"lans,omitempty"`
	// HostKey is the built-in SSH server's host key (authorized_keys
	// format), so peers pin it instead of trusting it on first use.
	HostKey string `json:"hostKey,omitempty"`
}

// PairRequest asks a device to trust the caller.
type PairRequest struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	PublicKey string `json:"publicKey"`
}

// Pair statuses.
const (
	PairApproved = "approved"
	PairPending  = "pending"
	PairDenied   = "denied"
)

// PairResponse is the answer to a pairing request or status poll.
type PairResponse struct {
	Status  string `json:"status"`
	ID      string `json:"id,omitempty"`
	User    string `json:"user,omitempty"`
	Home    string `json:"home,omitempty"`
	SSH     bool   `json:"ssh"`
	SSHPort int    `json:"sshPort,omitempty"`
	Message string `json:"message,omitempty"`
	// Code is the pairing confirmation code shown on both devices.
	Code string `json:"code,omitempty"`
}

// Service is a TCP port something is listening on.
type Service struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	// Label is a friendly guess at what it is ("Jupyter", "Ollama"…).
	Label string `json:"label,omitempty"`
	// Web reports whether it likely serves a web page.
	Web bool `json:"web"`
}

// Process is a running process.
type Process struct {
	PID    int     `json:"pid"`
	Name   string  `json:"name"`
	User   string  `json:"user,omitempty"`
	CPU    float64 `json:"cpu"`
	Mem    float64 `json:"mem"` // percent of RAM
	RSS    uint64  `json:"rss"`
	GPUMiB float64 `json:"gpuMiB,omitempty"`
	Cmd    string  `json:"cmd,omitempty"`
}

// Container is a Docker container.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
	Ports  string `json:"ports,omitempty"`
}

// Clipboard carries clipboard text.
type Clipboard struct {
	Text string `json:"text"`
}

// FileResult reports where a received file was saved.
type FileResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Caller is the verified identity of a tailnet peer making a request.
type Caller struct {
	NodeID    string `json:"nodeID"`
	Name      string `json:"name"`
	LoginName string `json:"loginName"`
	UserID    int64  `json:"userID"`
	Tagged    bool   `json:"tagged"`
	Addr      string `json:"addr"`
}
