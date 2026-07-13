// Package evaluate runs materialized benchmark cases through the analyzer.
package evaluate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iuliandita/exposuremap/internal/analyze"
	"github.com/iuliandita/exposuremap/internal/benchmark"
	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
	"github.com/iuliandita/exposuremap/internal/review"
)

const parseTimeout = 5 * time.Second

// CaseReport combines immutable case metadata with one analyzer report.
type CaseReport struct {
	SchemaVersion    int            `json:"schema_version"`
	Case             benchmark.Case `json:"case"`
	Analyzer         report.Report  `json:"analyzer"`
	ObservedElements int            `json:"observed_elements"`
	ResolvedElements int            `json:"resolved_elements"`
}

// Outcome contains the review scorer result and the report shown to reviewers.
type Outcome struct {
	Result review.Result
	Report CaseReport
}

// Summary aggregates one deterministic benchmark run.
type Summary struct {
	Cases       int            `json:"cases"`
	Complete    int            `json:"complete"`
	Incomplete  int            `json:"incomplete"`
	Changes     int            `json:"changes"`
	Findings    int            `json:"findings"`
	Kinds       map[string]int `json:"kinds"`
	ChangeKinds map[string]int `json:"change_kinds"`
	Diagnostics map[string]int `json:"diagnostics"`
	Rules       map[string]int `json:"rules"`
}

// Bundle is the complete local artifact set for one benchmark run.
type Bundle struct {
	Results []review.Result
	Reports []CaseReport
	Summary Summary
}

// Build verifies and evaluates every corpus case in stable case-ID order.
func Build(ctx context.Context, cache string, corpus benchmark.Corpus) (Bundle, error) {
	if problems := benchmark.Validate(corpus); len(problems) > 0 {
		return Bundle{}, fmt.Errorf("invalid corpus: %s %s", problems[0].Field, problems[0].Message)
	}
	cases := slices.Clone(corpus.Cases)
	slices.SortFunc(cases, func(left, right benchmark.Case) int {
		return strings.Compare(left.ID, right.ID)
	})
	bundle := Bundle{Summary: Summary{
		Kinds:       make(map[string]int),
		ChangeKinds: make(map[string]int),
		Diagnostics: make(map[string]int),
		Rules:       make(map[string]int),
	}}
	for index, item := range cases {
		outcome, err := RunCase(ctx, cache, item)
		if err != nil {
			return Bundle{}, fmt.Errorf("evaluate case %s: %w", item.ID, err)
		}
		outcome.Result.SampledForHuman = (index+1)%10 == 0
		bundle.Results = append(bundle.Results, outcome.Result)
		bundle.Reports = append(bundle.Reports, outcome.Report)
		bundle.Summary.Cases++
		bundle.Summary.Kinds[string(item.Kind)]++
		if outcome.Result.Status == review.StatusComplete {
			bundle.Summary.Complete++
		} else {
			bundle.Summary.Incomplete++
		}
		for _, diagnostic := range outcome.Report.Analyzer.Diagnostics {
			bundle.Summary.Diagnostics[diagnostic]++
		}
		for _, change := range outcome.Report.Analyzer.Changes {
			bundle.Summary.Changes++
			bundle.Summary.ChangeKinds[string(change.Kind)]++
		}
		for _, finding := range outcome.Report.Analyzer.Findings {
			bundle.Summary.Findings++
			bundle.Summary.Rules[finding.RuleID]++
		}
	}
	return bundle, nil
}

// RunCase verifies and analyzes one materialized benchmark case.
func RunCase(ctx context.Context, cache string, item benchmark.Case) (Outcome, error) {
	if _, err := benchmark.VerifyMaterialized(cache, item); err != nil {
		return Outcome{}, err
	}
	composePaths := make([]string, 0, 1)
	for _, candidate := range item.Paths {
		if benchmark.ComposePath(candidate) {
			composePaths = append(composePaths, candidate)
		}
	}
	if len(composePaths) != 1 {
		return incompleteOutcome(
			item,
			fmt.Sprintf("case: expected exactly one Compose path; found %d Compose paths", len(composePaths)),
		), nil
	}

	base, baseError := parseRevision(ctx, filepath.Join(cache, item.ID, "base", filepath.FromSlash(composePaths[0])))
	head, headError := parseRevision(ctx, filepath.Join(cache, item.ID, "head", filepath.FromSlash(composePaths[0])))
	if baseError != nil || headError != nil {
		diagnostics := make([]string, 0, 2)
		if baseError != nil {
			diagnostics = append(diagnostics, "base: "+baseError.Error())
		}
		if headError != nil {
			diagnostics = append(diagnostics, "head: "+headError.Error())
		}
		return incompleteOutcome(item, diagnostics...), nil
	}

	analysis := analyze.Compare(base, head)
	analyzerReport := report.Complete(analysis.Changes, analysis.Findings)
	observed := len(base.Services) + len(head.Services)
	result := review.Result{
		CaseID:           item.ID,
		Status:           review.StatusComplete,
		EngineVersion:    report.AnalyzerVersion,
		RulePackVersion:  analyze.RulePackVersion,
		ObservedElements: observed,
		ResolvedElements: observed,
	}
	result.Findings = reviewFindings(item.ID, analyzerReport.Findings)
	for _, finding := range analyzerReport.Findings {
		if item.ExpectedBoundary != "" && finding.RuleID == item.ExpectedBoundary {
			result.DetectedBoundary = true
		}
	}
	return Outcome{
		Result: result,
		Report: CaseReport{
			SchemaVersion:    1,
			Case:             item,
			Analyzer:         analyzerReport,
			ObservedElements: observed,
			ResolvedElements: observed,
		},
	}, nil
}

func parseRevision(parent context.Context, filePath string) (graph.Revision, error) {
	ctx, cancel := context.WithTimeout(parent, parseTimeout)
	defer cancel()
	return composeparser.ParseFile(ctx, filePath)
}

func incompleteOutcome(item benchmark.Case, diagnostics ...string) Outcome {
	analyzerReport := report.Incomplete(diagnostics...)
	return Outcome{
		Result: review.Result{
			CaseID:          item.ID,
			Status:          review.StatusIncomplete,
			EngineVersion:   report.AnalyzerVersion,
			RulePackVersion: analyze.RulePackVersion,
			Findings:        []review.Finding{},
		},
		Report: CaseReport{SchemaVersion: 1, Case: item, Analyzer: analyzerReport},
	}
}

func reviewFindings(caseID string, findings []report.Finding) []review.Finding {
	result := make([]review.Finding, 0, len(findings))
	for _, finding := range findings {
		digest := sha256.Sum256([]byte(strings.Join(
			[]string{finding.RuleID, finding.Service, finding.Evidence}, "\x00",
		)))
		result = append(result, review.Finding{
			ID:     caseID + "-" + hex.EncodeToString(digest[:6]),
			RuleID: finding.RuleID,
			Level:  review.LevelWarning,
		})
	}
	return result
}
