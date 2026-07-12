package main

import (
	"os"

	"github.com/iuliandita/exposuremap/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
