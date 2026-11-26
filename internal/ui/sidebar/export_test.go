package sidebar

import "gioui.org/widget"

// OpenReply and ReplyEditor let the package's outside tests, which can
// draw chatin's / menu on the sheet, open it and type into it.
func (s *Sidebar) OpenReply(ws, pane string)   { s.openReply(ws, pane) }
func (s *Sidebar) ReplyEditor() *widget.Editor { return &s.reply.ed }
