package app

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/nowindow"
)

// notifyTimeout covers Windows PowerShell's cold start, which the
// one-shot toast pays when the preloaded one is not running.
const notifyTimeout = 10 * time.Second

// toastHead loads the WinRT toast types under PowerShell's registered app
// id, which needs no install step, and defines Show-Toast. A toast with a
// tag replaces the one before it with the same tag: one per tab. The WinRT
// type syntax works in Windows PowerShell only, not pwsh.
const toastHead = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe')
function Show-Toast($title, $body, $tag) {
	$x = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
	$t = $x.GetElementsByTagName('text')
	$t.Item(0).AppendChild($x.CreateTextNode($title)) > $null
	$t.Item(1).AppendChild($x.CreateTextNode($body)) > $null
	$n = [Windows.UI.Notifications.ToastNotification]::new($x)
	if ($tag) { $n.Tag = $tag; $n.Group = 'pitwall' }
	$notifier.Show($n)
}
`

// toastScript shows one toast. The text comes in through the environment,
// so it needs no quoting.
const toastScript = toastHead + `Show-Toast $env:PITWALL_TOAST_TITLE $env:PITWALL_TOAST_BODY $env:PITWALL_TOAST_TAG`

// toasterScript shows a toast for each toastLine on stdin until it closes.
const toasterScript = toastHead + toastLoop

const toastLoop = `while ($null -ne ($line = [Console]::In.ReadLine())) {
	try { $m = ConvertFrom-Json $line; Show-Toast $m.title $m.body $m.tag } catch { [Console]::Error.WriteLine($_) }
}`

// desktopCommand shows one toast through a PowerShell of its own. Windows
// has no urgency here.
func desktopCommand(ctx context.Context, _ bool, group, title, body string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "PITWALL_TOAST_TITLE="+title, "PITWALL_TOAST_BODY="+body, "PITWALL_TOAST_TAG="+toastTag(group))
	// A hidden console of its own: -WindowStyle Hidden would hide the
	// terminal the GUI was started from.
	nowindow.Set(cmd)
	return cmd
}

// toaster is the PowerShell running toasterScript, which pays the cold
// start of about 10s once, when the window opens, instead of per toast.
var toaster struct {
	sync.Mutex
	in io.WriteCloser // nil while none runs
}

// preloadToasts starts the toaster unless it runs.
func preloadToasts() {
	toaster.Lock()
	defer toaster.Unlock()
	if toaster.in != nil {
		return
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toasterScript)
	nowindow.Set(cmd)
	cmd.Stderr = log.Writer()
	in, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		log.Printf("toasts: %q", err)
		return
	}
	toaster.in = in
	go func() {
		err := cmd.Wait()
		log.Printf("toasts: PowerShell exited: %v", err)
		toaster.Lock()
		if toaster.in == in {
			toaster.in = nil
		}
		toaster.Unlock()
	}()
}

// runDesktop hands the toast cmd describes, by the environment
// desktopCommand gave it, to the toaster, and runs cmd itself only when the
// toaster cannot take it. A toaster that is gone starts again for the next
// toast.
// ponytail: a toaster that stops reading would block here once the pipe
// fills; a write deadline would fall back instead.
func runDesktop(cmd *exec.Cmd) error {
	var title, body, tag string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "PITWALL_TOAST_TITLE="); ok {
			title = v
		} else if v, ok := strings.CutPrefix(kv, "PITWALL_TOAST_BODY="); ok {
			body = v
		} else if v, ok := strings.CutPrefix(kv, "PITWALL_TOAST_TAG="); ok {
			tag = v
		}
	}
	toaster.Lock()
	in := toaster.in
	if in != nil {
		if _, err := in.Write(toastLine(title, body, tag)); err != nil {
			in.Close()
			toaster.in, in = nil, nil
		}
	}
	toaster.Unlock()
	if in == nil {
		go preloadToasts()
		return cmd.Run()
	}
	return nil
}
