package benchmark_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/benchmark"
)

func TestValidateAcceptsValidCorpus(t *testing.T) {
	if problems := benchmark.Validate(validCorpus()); len(problems) != 0 {
		t.Fatalf("Validate() problems = %#v, want none", problems)
	}
}

func TestValidateRejectsInvalidCaseFields(t *testing.T) {
	corpus := validCorpus()
	corpus.SchemaVersion = 2
	corpus.Cases[0] = benchmark.Case{
		ID:         "",
		Repository: "git://github.com/example/project",
		LicenseURL: "",
		BaseSHA:    strings.Repeat("A", 40),
		HeadSHA:    strings.Repeat("A", 40),
		Kind:       "invented",
		Split:      "secret",
		Paths:      []string{"../compose.yml", "../compose.yml"},
	}

	problems := benchmark.Validate(corpus)
	fields := problemFields(problems)
	want := []string{
		"schema_version",
		"base_sha",
		"head_sha",
		"id",
		"kind",
		"license_url",
		"paths",
		"paths",
		"repository",
		"split",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("problem fields = %#v, want %#v; problems = %#v", fields, want, problems)
	}
}

func TestValidateRejectsDuplicateIDs(t *testing.T) {
	corpus := validCorpus()
	second := corpus.Cases[0]
	second.HeadSHA = strings.Repeat("c", 40)
	corpus.Cases = append(corpus.Cases, second)

	problems := benchmark.Validate(corpus)
	if !hasProblem(problems, second.ID, "id", "duplicate") {
		t.Fatalf("Validate() problems = %#v, want duplicate ID", problems)
	}
}

func TestValidateRequiresExpectedBoundaryForMutation(t *testing.T) {
	corpus := validCorpus()
	corpus.Cases[0].Kind = benchmark.CaseMutation

	problems := benchmark.Validate(corpus)
	if !hasProblem(problems, corpus.Cases[0].ID, "expected_boundary", "required") {
		t.Fatalf("Validate() problems = %#v, want required expected boundary", problems)
	}
}

func TestValidateKeepsRepositoryInOneSplit(t *testing.T) {
	corpus := validCorpus()
	second := corpus.Cases[0]
	second.ID = "immich-pr-101"
	second.HeadSHA = strings.Repeat("c", 40)
	second.Split = benchmark.SplitEvaluation
	corpus.Cases = append(corpus.Cases, second)

	problems := benchmark.Validate(corpus)
	if !hasProblem(problems, second.ID, "split", "repository") {
		t.Fatalf("Validate() problems = %#v, want repository split leak", problems)
	}
}

func TestValidateReturnsProblemsInStableOrder(t *testing.T) {
	corpus := validCorpus()
	corpus.Cases = []benchmark.Case{
		{ID: "z-case"},
		{ID: "a-case"},
	}

	first := benchmark.Validate(corpus)
	second := benchmark.Validate(corpus)
	if !slices.Equal(first, second) {
		t.Fatalf("Validate() changed order: first=%#v second=%#v", first, second)
	}
	for i := 1; i < len(first); i++ {
		previous := first[i-1].CaseID + "\x00" + first[i-1].Field + "\x00" + first[i-1].Message
		current := first[i].CaseID + "\x00" + first[i].Field + "\x00" + first[i].Message
		if previous > current {
			t.Fatalf("problems are not sorted at %d: %#v", i, first)
		}
	}
}

func validCorpus() benchmark.Corpus {
	return benchmark.Corpus{
		SchemaVersion: 1,
		Cases: []benchmark.Case{{
			ID:         "immich-pr-100",
			Repository: "https://github.com/immich-app/immich",
			LicenseURL: "https://github.com/immich-app/immich/blob/main/LICENSE",
			BaseSHA:    strings.Repeat("a", 40),
			HeadSHA:    strings.Repeat("b", 40),
			Kind:       benchmark.CaseReal,
			Split:      benchmark.SplitTraining,
			Paths:      []string{"docker/docker-compose.yml"},
		}},
	}
}

func problemFields(problems []benchmark.Problem) []string {
	fields := make([]string, 0, len(problems))
	for _, problem := range problems {
		fields = append(fields, problem.Field)
	}
	return fields
}

func hasProblem(problems []benchmark.Problem, caseID, field, messagePart string) bool {
	for _, problem := range problems {
		if problem.CaseID == caseID && problem.Field == field && strings.Contains(problem.Message, messagePart) {
			return true
		}
	}
	return false
}
