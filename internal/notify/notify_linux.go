package notify

import (
	"errors"
	"os/exec"
	"strings"
)

func send(title, body, _ string) error {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return errors.New("notify-send not found")
	}
	// notify-send bodies are markup: escape it so peers can't spoof content.
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return exec.Command(bin, "--app-name=TailConnect", "--icon=tailconnect", "--", clean(title), esc.Replace(clean(body))).Run()
}
