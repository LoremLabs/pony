//go:build !windows

package cli

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// execTxco replaces pony with txco: it keeps the terminal, its signals and
// the folder, and serves on after the browser is done.
func execTxco(path string, argv []string, stderr io.Writer) int {
	err := syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(stderr, "pony: couldn't start %s: %v\n", path, err)
	return ExitError
}
