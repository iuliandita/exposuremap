package review

// Role identifies a reviewer's place in the blinded review protocol.
type Role string

const (
	RolePrimary     Role = "primary"
	RoleSecondary   Role = "secondary"
	RoleAdjudicator Role = "adjudicator"
)

// Status records whether an analyzer produced a complete graph.
type Status string

const (
	StatusComplete   Status = "complete"
	StatusIncomplete Status = "incomplete"
)

// Level identifies whether a finding warns or proposes CI failure.
type Level string

const (
	LevelWarning Level = "warning"
	LevelFailure Level = "failure"
)

// Finding is the minimum analyzer output needed for benchmark scoring.
type Finding struct {
	ID     string `json:"id"`
	RuleID string `json:"rule_id"`
	Level  Level  `json:"level"`
}

// Result records analyzer output and graph-resolution measurements for one case.
type Result struct {
	CaseID           string    `json:"case_id"`
	Status           Status    `json:"status"`
	ObservedElements int       `json:"observed_elements"`
	ResolvedElements int       `json:"resolved_elements"`
	ProposedFailure  bool      `json:"proposed_failure"`
	SampledForHuman  bool      `json:"sampled_for_human"`
	DetectedBoundary bool      `json:"detected_boundary"`
	Findings         []Finding `json:"findings"`
}

// FindingReview records one reviewer's judgment of one emitted finding.
type FindingReview struct {
	FindingID         string `json:"finding_id"`
	Correct           bool   `json:"correct"`
	SeverityJustified bool   `json:"severity_justified"`
}

// Assessment is one blind review of a case report.
type Assessment struct {
	SchemaVersion       int             `json:"schema_version"`
	CaseID              string          `json:"case_id"`
	Reviewer            string          `json:"reviewer"`
	Role                Role            `json:"role"`
	FactuallyCorrect    bool            `json:"factually_correct"`
	SeverityJustified   bool            `json:"severity_justified"`
	UsefulBeyondRawDiff bool            `json:"useful_beyond_raw_diff"`
	MissedChange        bool            `json:"missed_change"`
	UnsupportedClaims   []string        `json:"unsupported_claims,omitempty"`
	FindingReviews      []FindingReview `json:"finding_reviews"`
	Notes               string          `json:"notes,omitempty"`
}

// Ratio stores an exact fraction. Comparisons do not use floating point.
type Ratio struct {
	Numerator   int `json:"numerator"`
	Denominator int `json:"denominator"`
}

// Report contains the technical gate measurements and decision.
type Report struct {
	Completeness        Ratio    `json:"completeness"`
	Resolution          Ratio    `json:"resolution"`
	WarningPrecision    Ratio    `json:"warning_precision"`
	UsefulnessAgreement Ratio    `json:"usefulness_agreement"`
	MutationRecall      Ratio    `json:"mutation_recall"`
	FalseFailures       int      `json:"false_failures"`
	Passed              bool     `json:"passed"`
	BlockingReasons     []string `json:"blocking_reasons"`
}
