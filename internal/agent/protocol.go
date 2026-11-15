// Package agent implements the device-to-device API every TailConnect node
// serves on its tailnet address. It is how peers discover each other, pair
// (exchange SSH trust), read resource stats and open port tunnels.
//
// All traffic rides the tailnet (WireGuard-encrypted); callers are
// identified with Tailscale WhoIs, never with anything they claim.
package agent

// Port is the tailnet TCP port of the agent API.
const Port = 4747

// ProtocolVersion is bumped on incompatible API changes.
const ProtocolVersion = 1

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
