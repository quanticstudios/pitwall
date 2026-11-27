package model

import (
	"strconv"
	"time"
)

// AwayAfter is how long no window or phone may show a chat before coming
// back to it brings up its Since you left card.
const AwayAfter = 5 * time.Minute

// Away is a chat's Since you left card: what its agent did while no
// window had the chat on screen and no phone had it open, from the
// daemon's log of the chat, and with [chat] away_summary on, two lines
// from the agent's cheapest model. The daemon keeps one per chat until
// the user sends, dismisses it, or scrolls through what they missed.
type Away struct {
	Chat  string    `json:"chat"`
	Since time.Time `json:"since"` // when the user last saw the chat
	At    time.Time `json:"at"`    // when they came back
	// Replies are the agent's messages since, in Turns turns.
	Replies int `json:"replies"`
	Turns   int `json:"turns"`
	// Edited are the files the agent edited, newest last, up to a few;
	// EditedN counts them all.
	Edited  []string `json:"edited,omitempty"`
	EditedN int      `json:"editedN,omitempty"`
	// Commands counts the commands it ran; Failed is the last one that
	// failed, redacted, "" for none.
	Commands int    `json:"commands,omitempty"`
	Failed   string `json:"failed,omitempty"`
	// Tests is the last test run's outcome: "passed", "failed" or "".
	Tests string `json:"tests,omitempty"`
	// Item is the first message the user missed, by its id in the chat's
	// snapshot; Seq is the same one as the phone numbers its entries.
	Item string `json:"item"`
	Seq  int64  `json:"seq"`
	// Status and Needs are the summary: the state of the work and what it
	// needs from the user. Asking says the summary is on its way.
	Status string `json:"status,omitempty"`
	Needs  string `json:"needs,omitempty"`
	Asking bool   `json:"asking,omitempty"`
}

// AwayNow is the agent's state as the card names it: "Working", "Needs
// you" or "Done"; "" when it says nothing.
func AwayNow(s AgentState) string {
	switch s {
	case StateWorking, StateConnecting:
		return "Working"
	case StatePendingApproval, StateAwaitingInput, StatePlanReady, StateError:
		return "Needs you"
	case StateCompleted, StatePROpen, StateParked:
		return "Done"
	}
	return ""
}

// AwayFor is how long d is as the card says it: "7m", "2h 14m", "3d".
func AwayFor(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 60:
		return strconv.Itoa(max(1, m)) + "m"
	case m < 24*60:
		if m%60 == 0 {
			return strconv.Itoa(m/60) + "h"
		}
		return strconv.Itoa(m/60) + "h " + strconv.Itoa(m%60) + "m"
	}
	return strconv.Itoa(m/(24*60)) + "d"
}

// AwayFacts are a card's facts as short phrases, in the card's order:
// "3 messages in 2 turns", "4 files edited", "12 commands", "tests
// passed".
func AwayFacts(a Away) []string {
	var out []string
	plural := func(n int, one string) string {
		s := strconv.Itoa(n) + " " + one
		if n != 1 {
			s += "s"
		}
		return s
	}
	if a.Replies > 0 {
		s := plural(a.Replies, "message")
		if a.Turns > 1 {
			s += " in " + plural(a.Turns, "turn")
		}
		out = append(out, s)
	}
	if a.EditedN > 0 {
		out = append(out, plural(a.EditedN, "file")+" edited")
	}
	if a.Commands > 0 {
		out = append(out, plural(a.Commands, "command"))
	}
	if a.Tests != "" {
		out = append(out, "tests "+a.Tests)
	}
	return out
}
