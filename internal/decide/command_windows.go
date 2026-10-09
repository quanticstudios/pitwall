package decide

import (
	"os/exec"

	"github.com/quanticstudios/pitwall/internal/nowindow"
)

// ownGroup leaves cmd's default cancel, which kills the process itself, and
// keeps the command's console hidden.
// ponytail: grandchildren may outlive the timeout on Windows; a job object
// would take them too.
func ownGroup(cmd *exec.Cmd) { nowindow.Set(cmd) }
