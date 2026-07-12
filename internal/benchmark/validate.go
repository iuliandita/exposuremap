package benchmark

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
)

const corpusProblemID = "$corpus"

// Validate returns every problem in a stable order.
func Validate(corpus Corpus) []Problem {
	problems := make([]Problem, 0)
	if corpus.SchemaVersion != 1 {
		problems = append(problems, Problem{
			CaseID:  corpusProblemID,
			Field:   "schema_version",
			Message: "must be 1",
		})
	}

	seenIDs := make(map[string]struct{}, len(corpus.Cases))
	repositorySplits := make(map[string]Split)
	for index, item := range corpus.Cases {
		caseID := item.ID
		if caseID == "" {
			caseID = fmt.Sprintf("case[%d]", index)
			problems = addProblem(problems, caseID, "id", "is required")
		} else if _, exists := seenIDs[item.ID]; exists {
			problems = addProblem(problems, caseID, "id", "is a duplicate")
		} else {
			seenIDs[item.ID] = struct{}{}
		}

		if !isGitHubRepository(item.Repository) {
			problems = addProblem(problems, caseID, "repository", "must be an https://github.com/OWNER/REPOSITORY URL")
		}
		if item.LicenseURL == "" {
			problems = addProblem(problems, caseID, "license_url", "is required")
		}
		if strings.TrimSpace(item.LicenseSPDX) == "" {
			problems = addProblem(problems, caseID, "license_spdx", "is required")
		}

		baseValid := isSHA(item.BaseSHA)
		headValid := isSHA(item.HeadSHA)
		if !baseValid {
			problems = addProblem(problems, caseID, "base_sha", "must be a 40-character lowercase hexadecimal SHA")
		}
		if !headValid {
			problems = addProblem(problems, caseID, "head_sha", "must be a 40-character lowercase hexadecimal SHA")
		}
		if baseValid && headValid && item.BaseSHA == item.HeadSHA {
			problems = addProblem(problems, caseID, "head_sha", "must differ from base_sha")
		}

		if item.Kind != CaseReal && item.Kind != CaseMutation {
			problems = addProblem(problems, caseID, "kind", "must be real or mutation")
		}
		if item.Kind == CaseMutation && strings.TrimSpace(item.ExpectedBoundary) == "" {
			problems = addProblem(problems, caseID, "expected_boundary", "is required for mutation cases")
		}

		if item.Split != SplitTraining && item.Split != SplitValidation && item.Split != SplitEvaluation {
			problems = addProblem(problems, caseID, "split", "must be training, validation, or evaluation")
		} else if previous, exists := repositorySplits[item.Repository]; exists && previous != item.Split {
			problems = addProblem(problems, caseID, "split", "repository must remain in one split")
		} else {
			repositorySplits[item.Repository] = item.Split
		}

		if len(item.Paths) == 0 {
			problems = addProblem(problems, caseID, "paths", "must contain at least one path")
		}
		seenPaths := make(map[string]struct{}, len(item.Paths))
		for _, candidate := range item.Paths {
			if _, exists := seenPaths[candidate]; exists {
				problems = addProblem(problems, caseID, "paths", fmt.Sprintf("contains duplicate path %q", candidate))
				continue
			}
			if !isSafeRelativePath(candidate) {
				problems = addProblem(problems, caseID, "paths", fmt.Sprintf("contains unsafe path %q", candidate))
			}
			seenPaths[candidate] = struct{}{}
		}
	}

	slices.SortFunc(problems, func(left, right Problem) int {
		if result := strings.Compare(left.CaseID, right.CaseID); result != 0 {
			return result
		}
		if result := strings.Compare(left.Field, right.Field); result != 0 {
			return result
		}
		return strings.Compare(left.Message, right.Message)
	})
	return problems
}

func addProblem(problems []Problem, caseID, field, message string) []Problem {
	return append(problems, Problem{CaseID: caseID, Field: field, Message: message})
}

func isSHA(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isGitHubRepository(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func isSafeRelativePath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}
