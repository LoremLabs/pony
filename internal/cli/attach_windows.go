//go:build windows

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// execTxco runs txco on this terminal and waits for it: Windows has no exec.
func execTxco(path string, argv []string, stderr io.Writer) int {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(stderr, "pony: couldn't start %s: %v\n", path, err)
		return ExitError
	}
	return ExitOK
}
