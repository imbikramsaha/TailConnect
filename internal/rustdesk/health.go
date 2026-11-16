package rustdesk

import (
	"net"
	"strconv"
	"time"
)

// Listening reports whether RustDesk accepts direct connections on this
// computer (what paired devices reach through TailConnect).
func Listening() bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(Port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
