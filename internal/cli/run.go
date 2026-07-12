package cli

import (
	"fmt"
	"io"
)

const usage = "usage: exposuremap benchmark <validate|mine|score>\n"

// Run executes the command and returns its process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}

	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	return 2
}
