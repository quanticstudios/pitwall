package remote

import (
	"os"
	"syscall"
)

// links is how many hard links the open file f has.
func links(f *os.File) (uint64, error) {
	var d syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &d); err != nil {
		return 0, err
	}
	return uint64(d.NumberOfLinks), nil
}
