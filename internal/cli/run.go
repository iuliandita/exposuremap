package cli

import (
	"fmt"
	"io"
)

const usage = `usage:
  exposuremap diff --base PATH --head PATH [--format json|markdown]
  exposuremap benchmark <validate|mine|score>
`

// Run executes the command and returns its process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithDependencies(args, stdout, stderr, defaultDependencies())
}

// RunWithDependencies executes the command with explicit external dependencies.
func RunWithDependencies(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if args[0] == "benchmark" {
		return runBenchmark(args[1:], stdout, stderr, normalizeDependencies(dependencies))
	}
	if args[0] == "diff" {
		return runDiff(args[1:], stdout, stderr)
	}

	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	return 2
}
