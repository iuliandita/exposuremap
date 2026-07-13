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
	"github.com/iuliandita/exposuremap/internal/projectconfig"
	"github.com/iuliandita/exposuremap/internal/report"
)

const parseTimeout = 5 * time.Second

func runDiff(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "path to the base Compose file")
	headPath := flags.String("head", "", "path to the head Compose file")
	contextPath := flags.String("context", "", "path to an explicit ExposureMap context file")
	format := flags.String("format", "json", "report format: json or markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *basePath == "" || *headPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "diff requires --base PATH --head PATH [--context PATH] [--format json|markdown]")
		return 2
	}
	if *format != "json" && *format != "markdown" {
		fmt.Fprintln(stderr, "--format must be json or markdown")
		return 2
	}
	variables := projectconfig.Variables{}
	if *contextPath != "" {
		loaded, err := projectconfig.Load(*contextPath)
		if err != nil {
			fmt.Fprintf(stderr, "load context: %v\n", err)
			return 2
		}
		variables = loaded
	}

	base, baseError := parseRevision(*basePath, variables.Base)
	head, headError := parseRevision(*headPath, variables.Head)
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
		analysis := analyze.Compare(base, head)
		result = report.Complete(analysis.Changes, analysis.Findings)
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

func parseRevision(path string, variables map[string]string) (graph.Revision, error) {
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	return composeparser.ParseFileWithOptions(ctx, path, composeparser.ParseOptions{Variables: variables})
}
