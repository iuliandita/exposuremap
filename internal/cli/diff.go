package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iuliandita/exposuremap/internal/analyze"
	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/projectconfig"
	"github.com/iuliandita/exposuremap/internal/report"
)

const parseTimeout = 5 * time.Second

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runDiff(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	basePath := flags.String("base", "", "path to the base Compose file")
	headPath := flags.String("head", "", "path to the head Compose file")
	baseRoot := flags.String("base-root", "", "root containing base Compose imports")
	headRoot := flags.String("head-root", "", "root containing head Compose imports")
	var baseOverrides stringListFlag
	var headOverrides stringListFlag
	flags.Var(&baseOverrides, "base-override", "ordered base Compose override path (repeatable)")
	flags.Var(&headOverrides, "head-override", "ordered head Compose override path (repeatable)")
	contextPath := flags.String("context", "", "path to an explicit ExposureMap context file")
	format := flags.String("format", "json", "report format: json or markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *basePath == "" || *headPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "diff requires --base PATH --head PATH [project options] [--context PATH] [--format json|markdown]")
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

	base, baseError := parseRevision(composeparser.ProjectInput{
		Primary:   *basePath,
		Root:      *baseRoot,
		Overrides: append([]string(nil), baseOverrides...),
		Variables: variables.Base,
	})
	head, headError := parseRevision(composeparser.ProjectInput{
		Primary:   *headPath,
		Root:      *headRoot,
		Overrides: append([]string(nil), headOverrides...),
		Variables: variables.Head,
	})
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

func parseRevision(input composeparser.ProjectInput) (graph.Revision, error) {
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	return composeparser.ParseProject(ctx, input)
}
