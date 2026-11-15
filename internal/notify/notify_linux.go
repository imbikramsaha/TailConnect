package notify

import (
	"errors"
	"os/exec"
)

func send(title, body, _ string) error {
	bin, err := exec.LookPath("notify-send")
	if err != nil {
		return errors.New("notify-send not found")
	}
	return exec.Command(bin, "--app-name=TailConnect", "--icon=network-workgroup", title, body).Run()
}
