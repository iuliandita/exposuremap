package review

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iuliandita/exposuremap/internal/benchmark"
)

const (
	minimumCompletenessPercent = 70
	minimumResolutionPercent   = 90
	minimumPrecisionPercent    = 95
	minimumUsefulnessPercent   = 80
)

// Score validates independent assessments and calculates the technical gate.
func Score(corpus benchmark.Corpus, results []Result, assessments []Assessment) (Report, error) {
	errorsFound := make([]string, 0)
	if problems := benchmark.Validate(corpus); len(problems) > 0 {
		return Report{}, fmt.Errorf("invalid corpus: %s", problems[0].Message)
	}

	casesByID := make(map[string]benchmark.Case, len(corpus.Cases))
	for _, item := range corpus.Cases {
		casesByID[item.ID] = item
	}
	resultsByID := make(map[string]Result, len(results))
	for _, result := range results {
		if _, exists := casesByID[result.CaseID]; !exists {
			errorsFound = append(errorsFound, fmt.Sprintf("result references unknown case %s", result.CaseID))
			continue
		}
		if _, exists := resultsByID[result.CaseID]; exists {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has duplicate result", result.CaseID))
			continue
		}
		if result.Status != StatusComplete && result.Status != StatusIncomplete {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has invalid result status %q", result.CaseID, result.Status))
		}
		if result.ObservedElements < 0 || result.ResolvedElements < 0 || result.ResolvedElements > result.ObservedElements {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has invalid resolution counts", result.CaseID))
		}
		if problem := validateFindings(result); problem != "" {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s %s", result.CaseID, problem))
		}
		resultsByID[result.CaseID] = result
	}
	for caseID := range casesByID {
		if _, exists := resultsByID[caseID]; !exists {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has no result", caseID))
		}
	}

	assessmentsByCase := make(map[string]map[Role]Assessment)
	for _, assessment := range assessments {
		result, exists := resultsByID[assessment.CaseID]
		if !exists {
			errorsFound = append(errorsFound, fmt.Sprintf("assessment references unknown case %s", assessment.CaseID))
			continue
		}
		if assessment.SchemaVersion != 1 {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has unsupported assessment schema", assessment.CaseID))
		}
		if assessment.Reviewer == "" {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has unnamed reviewer", assessment.CaseID))
		}
		if assessment.Role != RolePrimary && assessment.Role != RoleSecondary && assessment.Role != RoleAdjudicator {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has invalid reviewer role %q", assessment.CaseID, assessment.Role))
			continue
		}
		if assessmentsByCase[assessment.CaseID] == nil {
			assessmentsByCase[assessment.CaseID] = make(map[Role]Assessment)
		}
		if _, duplicate := assessmentsByCase[assessment.CaseID][assessment.Role]; duplicate {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s has duplicate %s assessment", assessment.CaseID, assessment.Role))
			continue
		}
		if problem := validateFindingReviews(result.Findings, assessment.FindingReviews); problem != "" {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s %s reviewer %s", assessment.CaseID, assessment.Role, problem))
		}
		assessmentsByCase[assessment.CaseID][assessment.Role] = assessment
	}

	if len(errorsFound) > 0 {
		return Report{}, joinedError(errorsFound)
	}

	report := Report{
		Completeness: Ratio{Denominator: len(corpus.Cases)},
	}
	for _, item := range corpus.Cases {
		result := resultsByID[item.ID]
		if result.Status == StatusComplete {
			report.Completeness.Numerator++
			report.Resolution.Numerator += result.ResolvedElements
			report.Resolution.Denominator += result.ObservedElements
		}
		if item.Kind == benchmark.CaseMutation {
			report.MutationRecall.Denominator++
			if result.DetectedBoundary {
				report.MutationRecall.Numerator++
			}
		}
		if len(result.Findings) == 0 {
			continue
		}

		roles := assessmentsByCase[item.ID]
		primary, primaryExists := roles[RolePrimary]
		secondary, secondaryExists := roles[RoleSecondary]
		if !primaryExists || !secondaryExists {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s requires primary and secondary assessments", item.ID))
			continue
		}
		if primary.Reviewer == secondary.Reviewer {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s requires independent reviewers", item.ID))
			continue
		}

		adjudicator, hasAdjudicator := roles[RoleAdjudicator]
		requiresAdjudication := result.ProposedFailure || result.SampledForHuman || hasFailure(result.Findings) || disagree(primary, secondary)
		if requiresAdjudication && !hasAdjudicator {
			errorsFound = append(errorsFound, fmt.Sprintf("case %s requires adjudication", item.ID))
			continue
		}
		final := primary
		if hasAdjudicator {
			final = adjudicator
		}

		report.UsefulnessAgreement.Denominator++
		if primary.UsefulBeyondRawDiff && secondary.UsefulBeyondRawDiff {
			report.UsefulnessAgreement.Numerator++
		}
		finalFindings := findingReviewsByID(final.FindingReviews)
		for _, finding := range result.Findings {
			judgment := finalFindings[finding.ID]
			switch finding.Level {
			case LevelWarning:
				report.WarningPrecision.Denominator++
				if judgment.Correct {
					report.WarningPrecision.Numerator++
				}
			case LevelFailure:
				if !judgment.Correct || !judgment.SeverityJustified {
					report.FalseFailures++
				}
			}
		}
	}

	if len(errorsFound) > 0 {
		return Report{}, joinedError(errorsFound)
	}
	if !meetsPercent(report.Completeness, minimumCompletenessPercent) {
		report.BlockingReasons = append(report.BlockingReasons, "completeness is below 70 percent")
	}
	if !meetsPercent(report.Resolution, minimumResolutionPercent) {
		report.BlockingReasons = append(report.BlockingReasons, "resolved elements are below 90 percent")
	}
	if !meetsPercent(report.WarningPrecision, minimumPrecisionPercent) {
		report.BlockingReasons = append(report.BlockingReasons, "warning precision is below 95 percent")
	}
	if !meetsPercent(report.UsefulnessAgreement, minimumUsefulnessPercent) {
		report.BlockingReasons = append(report.BlockingReasons, "reviewer usefulness agreement is below 80 percent")
	}
	if report.FalseFailures != 0 {
		report.BlockingReasons = append(report.BlockingReasons, "one or more false CI failures remain")
	}
	report.Passed = len(report.BlockingReasons) == 0
	return report, nil
}

func validateFindings(result Result) string {
	seen := make(map[string]struct{}, len(result.Findings))
	for _, finding := range result.Findings {
		if finding.ID == "" || finding.RuleID == "" {
			return "has finding without ID or rule ID"
		}
		if finding.Level != LevelWarning && finding.Level != LevelFailure {
			return fmt.Sprintf("has finding %s with invalid level", finding.ID)
		}
		if _, duplicate := seen[finding.ID]; duplicate {
			return fmt.Sprintf("has duplicate finding %s", finding.ID)
		}
		seen[finding.ID] = struct{}{}
	}
	return ""
}

func validateFindingReviews(findings []Finding, reviews []FindingReview) string {
	expected := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		expected[finding.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(reviews))
	for _, item := range reviews {
		if _, exists := expected[item.FindingID]; !exists {
			return fmt.Sprintf("reviews unknown finding %s", item.FindingID)
		}
		if _, duplicate := seen[item.FindingID]; duplicate {
			return fmt.Sprintf("reviews finding %s twice", item.FindingID)
		}
		seen[item.FindingID] = struct{}{}
	}
	if len(seen) != len(expected) {
		return "does not review every finding"
	}
	return ""
}

func disagree(left, right Assessment) bool {
	if left.FactuallyCorrect != right.FactuallyCorrect ||
		left.SeverityJustified != right.SeverityJustified ||
		left.UsefulBeyondRawDiff != right.UsefulBeyondRawDiff ||
		left.MissedChange != right.MissedChange {
		return true
	}
	leftFindings := findingReviewsByID(left.FindingReviews)
	rightFindings := findingReviewsByID(right.FindingReviews)
	for findingID, leftReview := range leftFindings {
		rightReview := rightFindings[findingID]
		if leftReview.Correct != rightReview.Correct || leftReview.SeverityJustified != rightReview.SeverityJustified {
			return true
		}
	}
	return false
}

func findingReviewsByID(reviews []FindingReview) map[string]FindingReview {
	result := make(map[string]FindingReview, len(reviews))
	for _, item := range reviews {
		result[item.FindingID] = item
	}
	return result
}

func hasFailure(findings []Finding) bool {
	return slices.ContainsFunc(findings, func(finding Finding) bool {
		return finding.Level == LevelFailure
	})
}

func meetsPercent(ratio Ratio, percent int) bool {
	return ratio.Denominator > 0 && ratio.Numerator*100 >= ratio.Denominator*percent
}

func joinedError(errorsFound []string) error {
	slices.Sort(errorsFound)
	return fmt.Errorf("%s", strings.Join(errorsFound, "; "))
}
