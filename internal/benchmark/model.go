package benchmark

// CaseKind identifies whether a benchmark case came from history or mutation.
type CaseKind string

const (
	CaseReal     CaseKind = "real"
	CaseMutation CaseKind = "mutation"
)

// Split identifies the repository-level benchmark partition.
type Split string

const (
	SplitTraining   Split = "training"
	SplitValidation Split = "validation"
	SplitEvaluation Split = "evaluation"
)

// Corpus is a versioned collection of immutable base-to-head cases.
type Corpus struct {
	SchemaVersion int    `json:"schema_version"`
	Cases         []Case `json:"cases"`
}

// Case identifies a real or controlled configuration transition.
type Case struct {
	ID               string   `json:"id"`
	Repository       string   `json:"repository"`
	LicenseURL       string   `json:"license_url"`
	BaseSHA          string   `json:"base_sha"`
	HeadSHA          string   `json:"head_sha"`
	Kind             CaseKind `json:"kind"`
	Split            Split    `json:"split"`
	Paths            []string `json:"paths"`
	ExpectedBoundary string   `json:"expected_boundary,omitempty"`
}

// SeedManifest records repositories to inspect without copying their source.
type SeedManifest struct {
	SchemaVersion int    `json:"schema_version"`
	ObservedAt    string `json:"observed_at"`
	Repositories  []Seed `json:"repositories"`
}

// Seed describes one repository and the configuration paths worth mining.
type Seed struct {
	Repository     string   `json:"repository"`
	DefaultBranch  string   `json:"default_branch"`
	CandidatePaths []string `json:"candidate_paths"`
	Role           string   `json:"role"`
}

// Problem is one deterministic corpus-validation diagnostic.
type Problem struct {
	CaseID  string `json:"case_id"`
	Field   string `json:"field"`
	Message string `json:"message"`
}
