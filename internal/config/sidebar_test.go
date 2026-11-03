package config

import "testing"

func TestSidebar(t *testing.T) {
	for _, tc := range []struct {
		body    string
		want    SidebarSettings
		problem string
	}{
		{"", SidebarSettings{WorkingShelf: true, SessionPreview: true, SettleAfterDays: 3, SettleOnMerge: true}, ""},
		{"view = \"inbox\"\nworking_shelf = false\nsettle_after_days = 0\nsettle_on_merge = false\nsession_preview = false", SidebarSettings{Inbox: true}, ""},
		{`workers = "separate"`, SidebarSettings{SeparateWorkers: true, WorkingShelf: true, SessionPreview: true, SettleAfterDays: 3, SettleOnMerge: true}, ""},
		{`workers = "nested"`, SidebarSettings{WorkingShelf: true, SessionPreview: true, SettleAfterDays: 3, SettleOnMerge: true}, `config.toml:2: sidebar.workers: "nested" is not folded or separate; using folded`},
		{`view = "list"`, SidebarSettings{WorkingShelf: true, SessionPreview: true, SettleAfterDays: 3, SettleOnMerge: true}, `config.toml:2: sidebar.view: "list" is not tree or inbox; using tree`},
		{`settle_after_days = 1.5`, SidebarSettings{WorkingShelf: true, SessionPreview: true, SettleAfterDays: 3, SettleOnMerge: true}, `config.toml:2: sidebar.settle_after_days: 1.5 is not a whole number from 0 to 365`},
	} {
		s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[sidebar]\n"+tc.body+"\n"))
		if s.Sidebar != tc.want || msgs(probs) != tc.problem {
			t.Errorf("%q: %+v, problems %q", tc.body, s.Sidebar, msgs(probs))
		}
	}
}
