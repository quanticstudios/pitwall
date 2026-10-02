package daemon

import (
	"fmt"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func (d *Daemon) setAppearance(m proto.SetProjectAppearance) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.st.Projects {
		if d.st.Projects[i].ID == m.ProjectID {
			d.st.Projects[i].Icon, d.st.Projects[i].Color = m.Icon, m.Color
			d.changed()
			return nil
		}
	}
	return fmt.Errorf("no project %s", m.ProjectID)
}
