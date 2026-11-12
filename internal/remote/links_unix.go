//go:build unix

package remote

import (
	"os"
	"syscall"
)

// links is how many hard links the open file f has.
func links(f *os.File) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return 0, err
	}
	return uint64(st.Nlink), nil
}
