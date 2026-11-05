//go:build !windows

package cli

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// termSize is w's columns and rows, or 0, 0 when it won't say. Tests replace
// it.
var termSize = func(w io.Writer) (cols, rows int) {
	f, ok := w.(*os.File)
	if !ok {
		return 0, 0
	}
	var size struct{ rows, cols, x, y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size))); e != 0 {
		return 0, 0
	}
	return int(size.cols), int(size.rows)
}
