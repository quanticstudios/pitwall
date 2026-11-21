package config

import (
	"strings"
	"testing"
)

func TestChat(t *testing.T) {
	for _, tc := range []struct {
		body    string
		want    int
		claude  bool
		problem string
	}{
		{"", ChatStandard, true, ""},
		{`width = "comfortable"`, ChatComfortable, true, ""},
		{`width = "full"`, 0, true, ""},
		{`width = "wide"`, ChatStandard, true, `config.toml:2: chat.width: "wide" is not comfortable, standard or full; using standard`},
		{"claude = \"terminal\"\nwidth = \"full\"", 0, false, ""},
		{`claude = "tui"`, ChatStandard, true, `config.toml:2: chat.claude: "tui" is not chat or terminal; using chat`},
	} {
		s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[chat]\n"+tc.body+"\n"))
		if s.Chat.Width != tc.want || s.Chat.Claude != tc.claude || msgs(probs) != tc.problem {
			t.Errorf("%q: %+v, problems %q", tc.body, s.Chat, msgs(probs))
		}
	}
	for _, tc := range []struct {
		body             string
		steer, ctrlEnter bool
		problem          string
	}{
		{"", false, false, ""},
		{`follow_up = "steer"`, true, false, ""},
		{`send_key = "ctrl+enter"`, false, true, ""},
		{"follow_up = \"queue\"\nsend_key = \"enter\"", false, false, ""},
		{`follow_up = "now"`, false, false, `config.toml:2: chat.follow_up: "now" is not queue or steer; using queue`},
		{`send_key = "shift+enter"`, false, false, `config.toml:2: chat.send_key: "shift+enter" is not enter or ctrl+enter; using enter`},
	} {
		s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[chat]\n"+tc.body+"\n"))
		if s.Chat.Steer != tc.steer || s.Chat.CtrlEnter != tc.ctrlEnter || msgs(probs) != tc.problem {
			t.Errorf("%q: %+v, problems %q", tc.body, s.Chat, msgs(probs))
		}
	}
	if d := Default(); !strings.Contains(d, `follow_up = "queue"`) || !strings.Contains(d, `send_key = "enter"`) {
		t.Error("follow_up or send_key is missing from the default file")
	}
	if !strings.Contains(Default(), "[chat]") || !strings.Contains(string(Schema()), `"comfortable"`) || !strings.Contains(string(Schema()), `"terminal"`) {
		t.Error("[chat] is missing from the default file or the schema")
	}
}
