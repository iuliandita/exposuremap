package review_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iuliandita/exposuremap/internal/benchmark"
	"github.com/iuliandita/exposuremap/internal/review"
)

func TestScorePassesAtExactThresholds(t *testing.T) {
	corpus, results, assessments := exactGateFixture()

	report, err := review.Score(corpus, results, assessments)
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if !report.Passed || len(report.BlockingReasons) != 0 {
		t.Fatalf("Score() report = %#v, want passing", report)
	}
	assertRatio(t, "completeness", report.Completeness, 14, 20)
	assertRatio(t, "resolution", report.Resolution, 9, 10)
	assertRatio(t, "warning precision", report.WarningPrecision, 19, 20)
	assertRatio(t, "usefulness", report.UsefulnessAgreement, 8, 10)
}

func TestScoreRejectsDuplicateReviewerRole(t *testing.T) {
	corpus, results, assessments := exactGateFixture()
	assessments = append(assessments, assessments[0])

	_, err := review.Score(corpus, results, assessments)
	if err == nil || !strings.Contains(err.Error(), "duplicate primary assessment") {
		t.Fatalf("Score() error = %v, want duplicate assessment", err)
	}
}

func TestScoreRequiresIndependentReviewerIdentities(t *testing.T) {
	corpus, results, assessments := exactGateFixture()
	for index := range assessments {
		if assessments[index].CaseID == "case-01" {
			assessments[index].Reviewer = "same-reviewer"
		}
	}

	_, err := review.Score(corpus, results, assessments)
	if err == nil || !strings.Contains(err.Error(), "independent reviewers") {
		t.Fatalf("Score() error = %v, want reviewer independence error", err)
	}
}

func TestScoreRequiresAdjudicationOnDisagreement(t *testing.T) {
	corpus, results, assessments := exactGateFixture()
	for index := range assessments {
		if assessments[index].CaseID == "case-01" && assessments[index].Role == review.RoleSecondary {
			assessments[index].UsefulBeyondRawDiff = false
		}
	}

	_, err := review.Score(corpus, results, assessments)
	if err == nil || !strings.Contains(err.Error(), "case-01 requires adjudication") {
		t.Fatalf("Score() error = %v, want adjudication requirement", err)
	}
}

func TestScoreCountsAdjudicatedFalseFailure(t *testing.T) {
	corpus, results, assessments := exactGateFixture()
	results[0].Findings[0].Level = review.LevelFailure
	results[0].ProposedFailure = true
	assessments = append(assessments, review.Assessment{
		SchemaVersion:       1,
		CaseID:              "case-01",
		Reviewer:            "owner",
		Role:                review.RoleAdjudicator,
		FactuallyCorrect:    false,
		SeverityJustified:   false,
		UsefulBeyondRawDiff: true,
		FindingReviews: []review.FindingReview{
			{FindingID: "case-01-warning-1", Correct: false, SeverityJustified: false},
			{FindingID: "case-01-warning-2", Correct: true, SeverityJustified: true},
		},
	})

	report, err := review.Score(corpus, results, assessments)
	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if report.FalseFailures != 1 || report.Passed || !containsReason(report.BlockingReasons, "false CI failure") {
		t.Fatalf("Score() report = %#v, want one blocking false failure", report)
	}
}

func TestScoreBlocksValuesBelowThresholds(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]review.Result, []review.Assessment)
		wantReason string
	}{
		{
			name: "completeness",
			mutate: func(results []review.Result, _ []review.Assessment) {
				results[13].Status = review.StatusIncomplete
			},
			wantReason: "completeness",
		},
		{
			name: "resolution",
			mutate: func(results []review.Result, _ []review.Assessment) {
				results[0].ResolvedElements = 8
			},
			wantReason: "resolved elements",
		},
		{
			name: "precision",
			mutate: func(_ []review.Result, assessments []review.Assessment) {
				for index := range assessments {
					if assessments[index].CaseID == "case-09" {
						assessments[index].FindingReviews[1].Correct = false
						assessments[index].FactuallyCorrect = false
					}
				}
			},
			wantReason: "warning precision",
		},
		{
			name: "usefulness",
			mutate: func(_ []review.Result, assessments []review.Assessment) {
				for index := range assessments {
					if assessments[index].CaseID == "case-08" {
						assessments[index].UsefulBeyondRawDiff = false
					}
				}
			},
			wantReason: "usefulness",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corpus, results, assessments := exactGateFixture()
			test.mutate(results, assessments)

			report, err := review.Score(corpus, results, assessments)
			if err != nil {
				t.Fatalf("Score() error = %v", err)
			}
			if report.Passed || !containsReason(report.BlockingReasons, test.wantReason) {
				t.Fatalf("Score() report = %#v, want blocking reason containing %q", report, test.wantReason)
			}
		})
	}
}

func exactGateFixture() (benchmark.Corpus, []review.Result, []review.Assessment) {
	corpus := benchmark.Corpus{SchemaVersion: 1}
	results := make([]review.Result, 0, 20)
	assessments := make([]review.Assessment, 0, 20)
	for index := 0; index < 20; index++ {
		caseID := fmt.Sprintf("case-%02d", index+1)
		corpus.Cases = append(corpus.Cases, benchmark.Case{
			ID:          caseID,
			Repository:  fmt.Sprintf("https://github.com/example/repo-%02d", index+1),
			LicenseURL:  fmt.Sprintf("https://github.com/example/repo-%02d/blob/main/LICENSE", index+1),
			LicenseSPDX: "MIT",
			PullRequest: index + 1,
			MergedAt:    time.Date(2026, 7, index+1, 0, 0, 0, 0, time.UTC),
			BaseSHA:     strings.Repeat("a", 39) + string(rune('a'+index%6)),
			HeadSHA:     strings.Repeat("b", 39) + string(rune('a'+index%6)),
			Kind:        benchmark.CaseReal,
			Split:       benchmark.SplitTraining,
			Paths:       []string{"compose.yaml"},
		})

		result := review.Result{CaseID: caseID, Status: review.StatusIncomplete}
		if index < 14 {
			result.Status = review.StatusComplete
		}
		if index == 0 {
			result.ObservedElements = 10
			result.ResolvedElements = 9
		}
		if index < 10 {
			result.Findings = []review.Finding{
				{ID: caseID + "-warning-1", RuleID: "EXPOSURE001", Level: review.LevelWarning},
				{ID: caseID + "-warning-2", RuleID: "EXPOSURE002", Level: review.LevelWarning},
			}
			correctSecond := index != 9
			for _, reviewer := range []struct {
				name string
				role review.Role
			}{
				{name: "codex", role: review.RolePrimary},
				{name: "claude", role: review.RoleSecondary},
			} {
				assessments = append(assessments, review.Assessment{
					SchemaVersion:       1,
					CaseID:              caseID,
					Reviewer:            reviewer.name,
					Role:                reviewer.role,
					FactuallyCorrect:    correctSecond,
					SeverityJustified:   true,
					UsefulBeyondRawDiff: index < 8,
					FindingReviews: []review.FindingReview{
						{FindingID: caseID + "-warning-1", Correct: true, SeverityJustified: true},
						{FindingID: caseID + "-warning-2", Correct: correctSecond, SeverityJustified: true},
					},
				})
			}
		}
		results = append(results, result)
	}
	return corpus, results, assessments
}

func assertRatio(t *testing.T, name string, got review.Ratio, numerator, denominator int) {
	t.Helper()
	if got.Numerator != numerator || got.Denominator != denominator {
		t.Fatalf("%s = %#v, want %d/%d", name, got, numerator, denominator)
	}
}

func containsReason(reasons []string, part string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, part) {
			return true
		}
	}
	return false
}
