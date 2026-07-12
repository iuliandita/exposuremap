package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/iuliandita/exposuremap/internal/analyze"
	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

const parseTimeout = 5 * time.Second

func runDiff(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "path to the base Compose file")
	headPath := flags.String("head", "", "path to the head Compose file")
	format := flags.String("format", "json", "report format: json or markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *basePath == "" || *headPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "diff requires --base PATH --head PATH [--format json|markdown]")
		return 2
	}
	if *format != "json" && *format != "markdown" {
		fmt.Fprintln(stderr, "--format must be json or markdown")
		return 2
	}

	base, baseError := parseRevision(*basePath)
	head, headError := parseRevision(*headPath)
	var result report.Report
	if baseError != nil || headError != nil {
		diagnostics := make([]string, 0, 2)
		if baseError != nil {
			diagnostics = append(diagnostics, "base: "+baseError.Error())
		}
		if headError != nil {
			diagnostics = append(diagnostics, "head: "+headError.Error())
		}
		result = report.Incomplete(diagnostics...)
	} else {
		result = report.Complete(analyze.Compare(base, head))
	}

	var err error
	if *format == "markdown" {
		err = report.RenderMarkdown(stdout, result)
	} else {
		err = report.RenderJSON(stdout, result)
	}
	if err != nil {
		fmt.Fprintf(stderr, "write report: %v\n", err)
		return 2
	}
	return 0
}

func parseRevision(path string) (graph.Revision, error) {
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	return composeparser.ParseFile(ctx, path)
}
