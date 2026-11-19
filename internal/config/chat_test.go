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
	if !strings.Contains(Default(), "[chat]") || !strings.Contains(string(Schema()), `"comfortable"`) || !strings.Contains(string(Schema()), `"terminal"`) {
		t.Error("[chat] is missing from the default file or the schema")
	}
}
