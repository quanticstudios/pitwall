package proto

// TmuxLevel adds the opt-in tmux shim and FocusSession.Pane.
const TmuxLevel = 51

// Tmux runs one tmux command inside From's tab. Env carries only the shim's
// PATH, TMUX and PITWALL_SOCKET; split panes receive their own TMUX_PANE from the daemon.
type Tmux struct {
	From string
	Args []string
	Env  []string
}

// TmuxResult answers one Tmux request to its caller alone.
type TmuxResult struct {
	Output string
	Error  string
}
