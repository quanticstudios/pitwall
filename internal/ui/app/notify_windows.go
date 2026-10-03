package app

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// notifyTimeout covers Windows PowerShell's cold start.
const notifyTimeout = 10 * time.Second

// toastScript shows a toast under PowerShell's registered app id, which
// needs no install step. The text comes in through the environment, so it
// needs no quoting. The WinRT type syntax works in Windows PowerShell only,
// not pwsh.
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$t = $x.GetElementsByTagName('text')
$t.Item(0).AppendChild($x.CreateTextNode($env:PITWALL_TOAST_TITLE)) > $null
$t.Item(1).AppendChild($x.CreateTextNode($env:PITWALL_TOAST_BODY)) > $null
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($x))`

// desktopCommand shows one toast. Windows has no replace-by-group here, and
// no urgency.
func desktopCommand(ctx context.Context, _ bool, _, title, body string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "PITWALL_TOAST_TITLE="+title, "PITWALL_TOAST_BODY="+body)
	// A hidden console of its own: -WindowStyle Hidden would hide the
	// terminal the GUI was started from.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}
