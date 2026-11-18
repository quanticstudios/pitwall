package config

import (
	"strings"
	"testing"
)

func TestChat(t *testing.T) {
	for _, tc := range []struct {
		body    string
		want    int
		problem string
	}{
		{"", ChatStandard, ""},
		{`width = "comfortable"`, ChatComfortable, ""},
		{`width = "full"`, 0, ""},
		{`width = "wide"`, ChatStandard, `config.toml:2: chat.width: "wide" is not comfortable, standard or full; using standard`},
	} {
		s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[chat]\n"+tc.body+"\n"))
		if s.Chat.Width != tc.want || msgs(probs) != tc.problem {
			t.Errorf("%q: %+v, problems %q", tc.body, s.Chat, msgs(probs))
		}
	}
	if !strings.Contains(Default(), "[chat]") || !strings.Contains(string(Schema()), `"comfortable"`) {
		t.Error("[chat] is missing from the default file or the schema")
	}
}
