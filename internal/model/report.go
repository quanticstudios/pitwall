package model

import (
	"strconv"
	"time"
)

// Report is a worker's card in its orchestrator's chat: what one turn of
// a worker tab (a tab with a Parent) ended on. The worker's last line
// says DONE or BLOCKED: <why>; a turn that ended without either is still
// in progress.
type Report struct {
	Tab    string       `json:"tab"`  // the worker's tab
	Pane   string       `json:"pane"` // its agent's pane
	Name   string       `json:"name"` // the tab's title
	PR     int          `json:"pr,omitempty"`
	Status ReportStatus `json:"status"`
	// Line is one or two lines from the report: the reason a blocked
	// worker gave, else the report's first line. Body is the report
	// without its status line, cut, for the card opened in place.
	Line string    `json:"line,omitempty"`
	Body string    `json:"body,omitempty"`
	Turn uint64    `json:"turn"` // the pane's turn, one card each
	At   time.Time `json:"at"`
}

// ReportStatus is how a worker's turn ended.
type ReportStatus string

const (
	ReportDone     ReportStatus = "done"
	ReportProgress ReportStatus = "progress"
	ReportBlocked  ReportStatus = "blocked"
)

// Label is the status as the card names it.
func (s ReportStatus) Label() string {
	switch s {
	case ReportDone:
		return "Done"
	case ReportBlocked:
		return "Blocked"
	}
	return "In progress"
}

// Key tells one card from another: a pane's turn.
func (r Report) Key() string { return r.Pane + "#" + strconv.FormatUint(r.Turn, 10) }
