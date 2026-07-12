package main

import (
	"io"
	"os"

	"github.com/iuliandita/exposuremap/internal/cli"
	"github.com/sirupsen/logrus"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	logrus.SetOutput(io.Discard)
	return cli.Run(args, stdout, stderr)
}
