package app

import (
	"cmp"
	"log"
	"math"
	"strconv"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chatSizeHint is how long the chat header shows the size a key set.
const chatSizeHint = 1500 * time.Millisecond

// chatSize is the chat's text size: [font] chat_size, else ui_size.
func (u *ui) chatSize() float64 {
	return cmp.Or(u.cfg.Font.ChatSize, u.cfg.Font.UISize, config.DefaultUISize)
}

// codeScale is the chat's code size over its text size: [font]
// chat_code_size, which follows chat_size while unset.
func (u *ui) codeScale() float32 {
	size := u.chatSize()
	return float32(cmp.Or(u.cfg.Font.ChatCodeSize, size) / size)
}

// chatMetric is m scaled so the chat's text, spacing and controls draw at
// chat_size where the rest of the window draws at ui_size.
func (u *ui) chatMetric(m unit.Metric) unit.Metric {
	f := float32(u.chatSize() / cmp.Or(u.cfg.Font.UISize, config.DefaultUISize))
	m.PxPerDp *= f
	m.PxPerSp *= f
	return m
}

// nextChatSize is the size a chat_text_ action steps cur to; reset
// follows ui again.
func nextChatSize(action string, cur, ui float64) float64 {
	switch action {
	case "chat_text_bigger":
		return min(48, math.Round(cur)+1)
	case "chat_text_smaller":
		return max(6, math.Round(cur)-1)
	}
	return ui
}

// saveChatSize keeps size in path's [font] chat_size, as the Settings
// stepper does: the interface size removes the key, so it follows again.
func saveChatSize(path string, size, ui float64) error {
	if size == ui {
		return config.RemoveKey(path, "font", "chat_size")
	}
	return config.SetKey(path, "font", "chat_size", config.Number(size))
}

// stepChatSize runs a chat_text_ action: shows the new size now, writes
// it to config.toml, and names it in the chat header for a moment.
func (u *ui) stepChatSize(action string, now time.Time) {
	ui := cmp.Or(u.cfg.Font.UISize, config.DefaultUISize)
	size := nextChatSize(action, u.chatSize(), ui)
	u.cfg.Font.ChatSize, u.chatSizeAt = size, now
	if err := saveChatSize(cmp.Or(u.cfg.Path, config.Path()), size, ui); err != nil {
		log.Printf("chat size: %q", err)
	}
	if u.invalidate != nil {
		u.invalidate()
	}
}

// chatTextKeys reports whether the chat_text_ keys are the window's: the
// shown tab is a chat in its chat view, or its focused pane shows as one.
func (u *ui) chatTextKeys(st *model.State) bool {
	ws := findWorkspace(st, u.nav.workspace)
	if ws == nil {
		return false
	}
	if ws.Chat != "" {
		return !chatTerminal(st, ws.Chat) && u.tui[ws.Chat] == nil
	}
	return u.paneChatShown(st, ws, u.nav.focused())
}

// chatSizeLabel is the header's note of the size a key just set, while
// it shows; nil after.
func (u *ui) chatSizeLabel(gtx gl.Context) gl.Widget {
	if u.chatSizeAt.IsZero() || gtx.Now.Sub(u.chatSizeAt) >= chatSizeHint {
		return nil
	}
	gtx.Execute(op.InvalidateCmd{At: u.chatSizeAt.Add(chatSizeHint)})
	th, s := u.th, "Text "+strconv.FormatFloat(u.chatSize(), 'f', -1, 64)
	return func(gtx gl.Context) gl.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, s)
	}
}
