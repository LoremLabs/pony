// Command pony talks to your OnePony ponies from the terminal.
package main

import (
	"os"

	"github.com/loremlabs/pony/internal/cli"
)

// Stamped at release time with -ldflags "-X main.Version=… -X main.Commit=…
// -X main.BuildTime=… -X main.InstallMethod=release". A plain `go build`
// keeps these defaults.
var (
	Version       = "0.0.0-dev"
	Commit        = "dev"
	BuildTime     = ""
	InstallMethod = "source"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Build{
		Version:       Version,
		Commit:        Commit,
		BuildTime:     BuildTime,
		InstallMethod: InstallMethod,
	}, os.Stdout, os.Stderr))
}
