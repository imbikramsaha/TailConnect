package notify

import (
	"os"
	"os/exec"
	"syscall"
)

// The script is constant: title and body arrive via environment variables
// and are XML-escaped inside PowerShell. Never interpolate them into the
// script — PowerShell treats several Unicode quote characters as string
// delimiters, so escaping can't be done reliably from outside.
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$t = [System.Security.SecurityElement]::Escape($env:TC_NOTIFY_TITLE)
$b = [System.Security.SecurityElement]::Escape($env:TC_NOTIFY_BODY)
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml("<toast><visual><binding template=""ToastGeneric""><text>$t</text><text>$b</text></binding></visual></toast>")
$n = [Windows.UI.Notifications.ToastNotification]::new($x)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($n)`

func send(title, body, _ string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "TC_NOTIFY_TITLE="+clean(title), "TC_NOTIFY_BODY="+clean(body))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd.Run()
}
