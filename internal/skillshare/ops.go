package skillshare

// Adapted from skillshare (MIT): herdr/skillshare/mods/skillshare/operations.js

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Outcome is what an operation tells the user: Text, and Err when it
// failed.
type Outcome struct {
	Text string
	Err  bool
}

func done(s string) Outcome              { return Outcome{Text: s} }
func failed(s string) Outcome            { return Outcome{Text: s, Err: true} }
func failure(r Result, w string) Outcome { return failed(Failure(r, w)) }

// at is the folder a scope's command runs in.
func at(scope, project string) string {
	if scope == Project {
		return project
	}
	return ""
}

// Sync syncs every resource in each scope, and plugins too when one
// waits. step says what it does now.
func (c Client) Sync(ctx context.Context, scopes []string, plugins bool, project string, step func(string)) Outcome {
	for _, s := range scopes {
		step("Syncing " + s + "…")
		if r := c.Run(ctx, at(s, project), "sync", "--all", "--json", Flag(s)); r.Code != 0 {
			return failure(r, "skillshare sync")
		}
		if plugins {
			if r := c.Run(ctx, at(s, project), "sync", "plugins", "--json", Flag(s)); r.Code != 0 {
				return failure(r, "skillshare sync plugins")
			}
		}
	}
	if len(scopes) > 1 {
		return done("Synced project and global")
	}
	return done("Synced " + strings.Join(scopes, ""))
}

// UpdateItems updates each item, or with all every skill or agent of
// its kind in its scope.
func (c Client) UpdateItems(ctx context.Context, items []Item, all bool, project string, step func(string)) Outcome {
	n := 0
	for _, it := range items {
		if all {
			step(fmt.Sprintf("Updating all %s (%s)…", it.Kind, it.Scope))
		} else {
			step("Updating " + it.Label + "…")
		}
		r := c.Run(ctx, at(it.Scope, project), UpdateArgs(it, all)...)
		var j struct {
			Updated *int `json:"updated"`
			Items   []struct {
				Name, Status, Error, Message string
			} `json:"items"`
			Agents []struct {
				Name, Status, Error, Message string
			} `json:"agents"`
		}
		parsed := json.Unmarshal([]byte(r.Stdout), &j) == nil
		var blocked []string
		for _, i := range j.Items {
			if i.Status == "security_blocked" {
				blocked = append(blocked, i.Name)
			}
		}
		if len(blocked) > 0 {
			return failed("Audit blocked " + strings.Join(blocked, ", ") + ". Ask an agent to audit it before updating.")
		}
		if r.Code != 0 && !parsed {
			return failure(r, "skillshare update")
		}
		for _, i := range append(j.Items, j.Agents...) {
			if i.Status == "failed" {
				return failed("Could not update " + i.Name + ": " + firstOf(i.Error, i.Message, "see skillshare update"))
			}
		}
		if j.Updated != nil {
			n += *j.Updated
		} else {
			for _, a := range j.Agents {
				if a.Status == "updated" {
					n++
				}
			}
		}
	}
	if n == 0 {
		return done("Already up to date")
	}
	return done(fmt.Sprintf("Updated %d. Targets get it on the next sync.", n))
}

// Toggle enables or disables it (action "enable" or "disable"), then
// reads the list back: exit 0 says only that the entry's own line was
// written, and another pattern in the ignore file can still decide.
func (c Client) Toggle(ctx context.Context, it Item, action, project string, step func(string)) Outcome {
	args := ToggleArgs(it, action)
	if args == nil {
		return failed("This action is unavailable.")
	}
	verb := "Disabling "
	if action == "enable" {
		verb = "Enabling "
	}
	step(verb + it.Label + "…")
	dir := at(it.Scope, project)
	if r := c.Run(ctx, dir, args...); r.Code != 0 {
		return failure(r, "skillshare "+action)
	}
	want := action == "disable"
	var list []ListEntry
	if json.Unmarshal([]byte(c.Run(ctx, dir, "list", "--all", "--json", Flag(it.Scope)).Stdout), &list) == nil {
		kind := "skill"
		if it.Kind == Agents {
			kind = "agent"
		}
		for _, e := range list {
			if firstOf(e.Kind, "skill") == kind && firstOf(e.RelPath, e.Name) == it.Path && e.Disabled != want {
				file := ".skillignore"
				if kind == "agent" {
					file = ".agentignore"
				}
				state := "disabled"
				if want {
					state = "enabled"
				}
				return failed(fmt.Sprintf("%s is still %s: another pattern in %s or %s.local decides it. Edit that file.", it.Label, state, file, file))
			}
		}
	}
	if want {
		return done("Disabled " + it.Label + ". The next sync removes it from your targets.")
	}
	return done("Enabled " + it.Label + ". The next sync sends it to your targets.")
}

// Uninstall moves it to the CLI's trash.
func (c Client) Uninstall(ctx context.Context, it Item, project string, step func(string)) Outcome {
	step("Uninstalling " + it.Label + "…")
	r := c.Run(ctx, at(it.Scope, project), UninstallArgs(it)...)
	if r.Code != 0 {
		return failure(r, "skillshare uninstall")
	}
	var j struct {
		Failed []string `json:"failed"`
	}
	if json.Unmarshal([]byte(r.Stdout), &j) == nil && len(j.Failed) > 0 {
		return failed("Could not uninstall " + strings.Join(j.Failed, ", "))
	}
	return done("Moved " + it.Label + " to the skillshare trash")
}
