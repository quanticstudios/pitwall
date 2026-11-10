package sidebar

import "github.com/quanticstudios/pitwall/internal/model"

func (s *Sidebar) devURL(ws model.Workspace) string {
	if s.Host != "" || s.hostOf(ws.ID) != "" {
		return ""
	}
	return ws.DevURL(s.DevProxyPort)
}
