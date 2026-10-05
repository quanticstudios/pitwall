package decide

import "os/exec"

// ownGroup leaves cmd's default cancel, which kills the process itself.
// ponytail: grandchildren may outlive the timeout on Windows; a job object
// would take them too.
func ownGroup(cmd *exec.Cmd) {}
