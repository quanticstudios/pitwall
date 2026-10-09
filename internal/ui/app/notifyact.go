package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// actionWait is how long a notification with actions waits for a click.
const actionWait = time.Hour

// urgentActivity reports whether a notifies at critical urgency: an
// approval, an error, or what triage rates now.
func urgentActivity(a model.Activity) bool {
	return a.State == model.StatePendingApproval || a.State == model.StateError || a.Urgency == "now"
}

// notifySendArgs are notify-send's arguments for one notification. With
// actions, a click on it is the action "default", answer adds Allow and
// Deny ("allow", "deny"), and notify-send waits for the notification to
// close and prints the action taken.
func notifySendArgs(urgent, actions, answer bool, group, title, body string) []string {
	urgency := "normal"
	if urgent {
		urgency = "critical"
	}
	args := []string{"--app-name=pitwall", "--urgency=" + urgency, "--hint=string:x-canonical-private-synchronous:pitwall-" + group}
	if actions {
		args = append(args, "--wait", "--action=default=Open")
		if answer {
			args = append(args, "--action=allow=Allow", "--action=deny=Deny")
		}
	}
	return append(args, "--", title, body)
}

// notifyActions reports whether this notify-send takes --action and
// --wait, which libnotify 0.7.10 added. Older ones, and macOS and
// Windows, get plain notifications.
func notifyActions() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "notify-send", "--help").Output()
	return err == nil && bytes.Contains(out, []byte("--action")) && bytes.Contains(out, []byte("--wait"))
}

// waitAction starts cmd, a notify-send that waits, and passes the action
// it prints, if any, to act. done runs when it ends.
func waitAction(cmd *exec.Cmd, act func(string), done func()) {
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		done()
		log.Printf("%q: %q", cmd.Args[0], err)
		return
	}
	go func() {
		defer done()
		if cmd.Wait() == nil {
			if a := strings.TrimSpace(out.String()); a != "" {
				act(a)
			}
		}
	}()
}

// act runs the action clicked on a's notification: "default" shows its
// pane, "allow" and "deny" answer its prompt the way the sidebar does.
func (n *notifier) act(b Backend, a model.Activity, action string) {
	switch action {
	case "default":
		if n.jump != nil {
			n.jump(a)
		}
	case "allow", "deny":
		if err := b.Send(proto.Answer{Pane: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: action == "allow"}); err != nil {
			log.Printf("answer from a notification: %q", err)
		}
	}
}

// setRules sets the [notifications] rules for the next delivery.
func (n *notifier) setRules(r config.NotifySettings) {
	n.mu.Lock()
	n.rules = r
	n.mu.Unlock()
}

// notifyRule decides whether a, due to notify at now, shows under r, and
// whether it plays r's sound. Quiet hours keep the sound off and let only
// urgent ones (urgentActivity) through.
func notifyRule(r config.NotifySettings, a model.Activity, now time.Time) (show, sound bool) {
	if slices.Contains(r.MutedAgents, string(a.Provider)) {
		return false, false
	}
	show = true
	switch a.State {
	case model.StatePendingApproval, model.StatePlanReady:
		show = r.Approval
	case model.StateAwaitingInput:
		show = r.Input
	case model.StateCompleted:
		show = r.Done
	case model.StateError:
		show = r.Failed
	}
	quiet := r.Quiet(now)
	if quiet && !urgentActivity(a) {
		show = false
	}
	return show, show && !quiet && r.Sound != ""
}

// linkLevel is b's daemon's proto.Level.
func linkLevel(b Backend) int {
	if l, ok := b.(Linker); ok {
		return l.Link().Level
	}
	return proto.Level
}

var (
	// soundPlayers play a sound file: PulseAudio's, PipeWire's, macOS's.
	soundPlayers = []string{"paplay", "pw-play", "afplay"}
	// bellSounds are what sound = "bell" plays, the first that exists.
	bellSounds = []string{"/usr/share/sounds/freedesktop/stereo/bell.oga", "/System/Library/Sounds/Ping.aiff"}
)

// soundCommand plays sound, "bell" or a file's path, with the first
// player installed.
func soundCommand(sound string) (*exec.Cmd, error) {
	file := sound
	if sound == "bell" {
		file = ""
		for _, f := range bellSounds {
			if _, err := os.Stat(f); err == nil {
				file = f
				break
			}
		}
		if file == "" {
			return nil, errors.New("no bell sound installed; set sound to a file")
		}
	}
	for _, p := range soundPlayers {
		if path, err := exec.LookPath(p); err == nil {
			return exec.Command(path, file), nil
		}
	}
	return nil, errors.New("none of paplay, pw-play or afplay is installed")
}

// playSound starts sound and leaves it playing.
func playSound(sound string) error {
	cmd, err := soundCommand(sound)
	if err == nil {
		err = cmd.Start()
	}
	if err == nil {
		go cmd.Wait()
	}
	return err
}
