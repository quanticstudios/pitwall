package proto

// EditScrollback opens Pane's history and screen in the daemon's editor,
// in a split to its right. The file goes away when the editor exits.
type EditScrollback struct{ Pane string }

// Scrollback asks for Pane's history and screen as plain text, with soft
// wraps joined. Lines keeps the last N logical lines; 0 means all lines.
type Scrollback struct {
	Pane  string
	Lines int
}

// ScrollbackResult answers Scrollback for its requesting client alone.
type ScrollbackResult struct {
	Pane string
	Text string
}
