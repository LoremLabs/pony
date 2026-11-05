package cli

import "io"

// termSize is 0, 0 here: Windows gets the plain spinner.
var termSize = func(io.Writer) (cols, rows int) { return 0, 0 }
