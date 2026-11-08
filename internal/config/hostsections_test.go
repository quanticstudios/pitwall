package config

import (
	"strings"
	"testing"
)

func TestHostSections(t *testing.T) {
	for _, tc := range []struct {
		text  string
		want  bool
		probs int
	}{
		{"", false, 0},
		{"[appearance]\nhost_sections = \"divider\"\n", false, 0},
		{"[appearance]\nhost_sections = \"sections\"\n", true, 0},
		{"[appearance]\nhost_sections = \"boxes\"\n", false, 1},
	} {
		s, problems := LoadFile(write(t, t.TempDir(), "config.toml", tc.text))
		if len(problems) != tc.probs || s.HostSections != tc.want {
			t.Errorf("%q: sections %v, problems %v", tc.text, s.HostSections, problems)
		}
	}
	if !strings.Contains(Default(), `# host_sections = "divider"`) || !strings.Contains(string(Schema()), `"host_sections"`) {
		t.Fatal("host_sections missing from default config or schema")
	}
}
