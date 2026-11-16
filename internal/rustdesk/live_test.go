package rustdesk_test

import (
	"os"
	"testing"
	"time"

	"github.com/imbikramsaha/tailconnect/internal/launch"
	"github.com/imbikramsaha/tailconnect/internal/rustdesk"
)

// TestStartOnDemandLive checks what TailConnect does when a paired device
// connects to a Mac whose RustDesk isn't running: start it hidden and have
// it accept connections. The RustDesk setup workflow runs it after setup.
func TestStartOnDemandLive(t *testing.T) {
	if os.Getenv("TAILCONNECT_LIVE_RUSTDESK") == "" {
		t.Skip("needs RustDesk set up; TAILCONNECT_LIVE_RUSTDESK=1")
	}
	if launch.RustDeskRunning() {
		t.Fatal("quit RustDesk first")
	}
	if rustdesk.NeedsRepair() {
		t.Fatal("RustDesk's settings need repair")
	}
	if err := launch.StartRustDesk(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !rustdesk.Listening() {
		if time.Now().After(deadline) {
			t.Fatalf("RustDesk started (running=%v) but isn't listening on %d", launch.RustDeskRunning(), rustdesk.Port)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !launch.RustDeskRunning() {
		t.Error("listening, but RustDesk isn't reported as running")
	}
}
