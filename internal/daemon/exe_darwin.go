//go:build darwin && cgo

package daemon

// #include <libproc.h>
import "C"

import "unsafe"

// readExe is pid's executable path, or "", from proc_pidpath. Gio already
// needs cgo on macOS, so this adds no build requirement.
func readExe(pid int) string {
	var buf [4096]byte // PROC_PIDPATHINFO_MAXSIZE
	n := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
	if n <= 0 {
		return ""
	}
	return string(buf[:n])
}
