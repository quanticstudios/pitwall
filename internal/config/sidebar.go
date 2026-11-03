package config

import "fmt"

// Sidebar is [sidebar]: the tree or the inbox, and how the inbox sorts.
type Sidebar struct {
	View            string   `toml:"view" enum:"tree,inbox" doc:"tree lists tabs under their groups; inbox lists every agent tab in one list by what it needs from you: Pinned, Waiting on you, Working, Snoozed and Settled. The sidebar's header switches between them too. See docs/usage.md, The inbox"`
	Workers         string   `toml:"workers" enum:"folded,separate" doc:"folded lists the tabs an agent opened as compact sub-rows of its tab's row, with a count; separate gives each its own row under its tab, behind a bar of their states, and its own row in the inbox. See docs/usage.md, Workers"`
	WorkingShelf    *bool    `toml:"working_shelf" doc:"In the inbox, move tabs whose agent works now to their own Working section, out of Waiting on you"`
	SessionPreview  *bool    `toml:"session_preview" doc:"Hover a session in the host list to preview its tabs, most urgent first"`
	SettleAfterDays *float64 `toml:"settle_after_days" min:"0" max:"365" doc:"Settle a tab after this many days without agent activity: it moves to the inbox's Settled section. 0 never settles one on its own. Pinned and snoozed tabs stay"`
	SettleOnMerge   *bool    `toml:"settle_on_merge" doc:"Settle a tab once pitwall sees its pull request merge. Pinned tabs stay"`
}

// DefaultSettleAfterDays is [sidebar] settle_after_days's default.
const DefaultSettleAfterDays = 3

// SidebarSettings is [sidebar] resolved.
type SidebarSettings struct {
	Inbox           bool // view = "inbox"
	SeparateWorkers bool // workers = "separate"
	WorkingShelf    bool
	SessionPreview  bool
	SettleAfterDays int // 0 never
	SettleOnMerge   bool
}

func defaultSidebar() Sidebar {
	on, days := true, float64(DefaultSettleAfterDays)
	return Sidebar{View: "tree", Workers: "folded", WorkingShelf: &on, SessionPreview: &on, SettleAfterDays: &days, SettleOnMerge: &on}
}

// resolveSidebar fills in [sidebar]'s values from c.
func resolveSidebar(c Sidebar) (SidebarSettings, []issue) {
	s := SidebarSettings{WorkingShelf: c.WorkingShelf == nil || *c.WorkingShelf, SettleAfterDays: DefaultSettleAfterDays,
		SessionPreview: c.SessionPreview == nil || *c.SessionPreview,
		SettleOnMerge:  c.SettleOnMerge == nil || *c.SettleOnMerge}
	var out []issue
	switch c.View {
	case "", "tree":
	case "inbox":
		s.Inbox = true
	default:
		out = append(out, issue{"sidebar.view", fmt.Sprintf("%q is not tree or inbox; using tree", c.View)})
	}
	switch c.Workers {
	case "", "folded":
	case "separate":
		s.SeparateWorkers = true
	default:
		out = append(out, issue{"sidebar.workers", fmt.Sprintf("%q is not folded or separate; using folded", c.Workers)})
	}
	if d := c.SettleAfterDays; d != nil {
		if *d < 0 || *d > 365 || *d != float64(int(*d)) {
			out = append(out, issue{"sidebar.settle_after_days", fmt.Sprintf("%g is not a whole number from 0 to 365", *d)})
		} else {
			s.SettleAfterDays = int(*d)
		}
	}
	return s, out
}
