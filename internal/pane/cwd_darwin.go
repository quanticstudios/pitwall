//go:build darwin && cgo

package pane

// #include <libproc.h>
// #include <string.h>
//
// static int pw_cwd(int pid, char *buf, int n) {
// 	struct proc_vnodepathinfo vpi;
// 	if (proc_pidinfo(pid, PROC_PIDVNODEPATHINFO, 0, &vpi, sizeof vpi) != sizeof vpi) return -1;
// 	strlcpy(buf, vpi.pvi_cdir.vip_path, n);
// 	return 0;
// }
import "C"

import "unsafe"

// procCwd is pid's working directory, or "", from proc_pidinfo: macOS has no
// procfs. Gio already needs cgo on macOS, so this adds no build requirement.
func procCwd(pid int) string {
	var buf [4096]byte
	if C.pw_cwd(C.int(pid), (*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))) != 0 {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}
