// Command apex is the apex-code CLI entrypoint.
//
// It supports three invocation modes (plan 9.1–9.3):
//
//   - one-shot:     apex "fix the failing test"
//   - pipe:         cat err.log | apex
//   - interactive:  apex            (launches the Bubble Tea TUI/REPL)
//
// All flag parsing, mode selection, and wiring live in internal/cli.
package main

import (
	"os"

	"github.com/apex-code/apex/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
