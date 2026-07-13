package analyze

import (
	"slices"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

func TestDataServiceImageUsesExactNormalizedBasename(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		{image: "postgres:16-alpine", want: true},
		{image: "registry.example/team/Postgres:16-alpine", want: true},
		{image: "registry.example:5000/team/postgres@sha256:abcdef", want: true},
		{image: "docker.io/library/clickhouse-server:24", want: true},
		{image: "adminer", want: true},
		{image: ""},
		{image: "custom-postgres"},
		{image: "postgres-backup"},
		{image: "team/database"},
		{image: "scratch"},
	}

	for _, test := range tests {
		t.Run(test.image, func(t *testing.T) {
			if got := dataServiceImage(test.image); got != test.want {
				t.Fatalf("dataServiceImage(%q) = %v, want %v", test.image, got, test.want)
			}
		})
	}
}

func TestCompareDetectsNewDataServicePath(t *testing.T) {
	source := exposedService("web", "backend")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend"}},
	)

	findings := Compare(base, head).Findings
	if len(findings) != 1 {
		t.Fatalf("findings = %#v, want one", findings)
	}
	want := report.Finding{
		RuleID:      "EM-COMPOSE-005",
		Title:       "Host-exposed service gained a data-service path",
		Severity:    "high",
		Confidence:  "medium",
		Service:     "web",
		Evidence:    "web -> backend -> db (postgres:16-alpine)",
		Base:        "data-service path absent",
		Head:        "web -> backend -> db (postgres:16-alpine)",
		Remediation: "Isolate the data service on a network the exposed service does not join, or document why the path is required.",
	}
	if findings[0] != want {
		t.Fatalf("finding = %#v, want %#v", findings[0], want)
	}
}

func TestCompareDetectsDataServicePathFromNewlyExposedSource(t *testing.T) {
	target := graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend"}}
	base := normalizedRevision(
		graph.Service{Name: "web", Networks: []string{"backend"}},
		target,
	)
	head := normalizedRevision(exposedService("web", "backend"), target)

	findings := findingsForRule(Compare(base, head).Findings, "EM-COMPOSE-005")
	if len(findings) != 1 {
		t.Fatalf("rule 005 findings = %#v, want one", findings)
	}
}

func TestCompareDoesNotTreatDuplicateServicesAsPureRenames(t *testing.T) {
	database := graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend"}}
	exposed := exposedService("web", "backend")

	tests := map[string]struct {
		base graph.Revision
		head graph.Revision
		want string
	}{
		"new duplicate source": {
			base: normalizedRevision(exposed, database),
			head: normalizedRevision(exposed, exposedService("api", "backend"), database),
			want: "api -> backend -> db (postgres:16-alpine)",
		},
		"new duplicate target": {
			base: normalizedRevision(exposed, database),
			head: normalizedRevision(
				exposed,
				database,
				graph.Service{Name: "replica", Image: "postgres:16-alpine", Networks: []string{"backend"}},
			),
			want: "web -> backend -> replica (postgres:16-alpine)",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			findings := findingsForRule(Compare(test.base, test.head).Findings, "EM-COMPOSE-005")
			if len(findings) != 1 || findings[0].Evidence != test.want {
				t.Fatalf("rule 005 findings = %#v, want evidence %q", findings, test.want)
			}
		})
	}
}

func TestCompareIgnoresNonWideningDataServicePaths(t *testing.T) {
	exposed := exposedService("web", "backend")
	internal := graph.Service{Name: "web", Networks: []string{"backend"}}
	database := graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend"}}

	tests := map[string]struct {
		base graph.Revision
		head graph.Revision
	}{
		"unrecognized image": {
			base: normalizedRevision(exposed),
			head: normalizedRevision(exposed, graph.Service{Name: "db", Image: "custom-postgres", Networks: []string{"backend"}}),
		},
		"internal source": {
			base: normalizedRevision(internal),
			head: normalizedRevision(internal, database),
		},
		"host-exposed target": {
			base: normalizedRevision(exposed),
			head: normalizedRevision(exposed, graph.Service{
				Name:     "db",
				Image:    "postgres:16-alpine",
				Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "5432", Target: 5432}},
				Networks: []string{"backend"},
			}),
		},
		"unchanged path": {
			base: normalizedRevision(exposed, database),
			head: normalizedRevision(exposed, database),
		},
		"second shared network": {
			base: normalizedRevision(
				exposedService("web", "legacy"),
				graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"legacy"}},
			),
			head: normalizedRevision(
				exposedService("web", "backend", "legacy"),
				graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"backend", "legacy"}},
			),
		},
		"path removal": {
			base: normalizedRevision(exposed, database),
			head: normalizedRevision(exposed, graph.Service{Name: "db", Image: "postgres:16-alpine", Networks: []string{"storage"}}),
		},
		"pure service renames": {
			base: normalizedRevision(
				exposedService("old-web", "backend"),
				graph.Service{Name: "old-db", Image: "postgres:16-alpine", Networks: []string{"backend"}},
			),
			head: normalizedRevision(exposed, database),
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if findings := findingsForRule(Compare(test.base, test.head).Findings, "EM-COMPOSE-005"); len(findings) != 0 {
				t.Fatalf("rule 005 findings = %#v, want none", findings)
			}
		})
	}
}

func TestCompareSortsDataServicePathsAndSelectsFirstNetwork(t *testing.T) {
	source := exposedService("web", "zeta", "alpha")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{Name: "z-db", Image: "redis:8", Networks: []string{"zeta", "alpha"}},
		graph.Service{Name: "a-db", Image: "postgres:16", Networks: []string{"zeta", "alpha"}},
	)

	findings := findingsForRule(Compare(base, head).Findings, "EM-COMPOSE-005")
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want two", findings)
	}
	want := []string{
		"web -> alpha -> a-db (postgres:16)",
		"web -> alpha -> z-db (redis:8)",
	}
	got := []string{findings[0].Evidence, findings[1].Evidence}
	if !slices.Equal(got, want) {
		t.Fatalf("evidence = %#v, want %#v", got, want)
	}
}

func normalizedRevision(services ...graph.Service) graph.Revision {
	return graph.Normalize(graph.Revision{Complete: true, Services: services})
}

func exposedService(name string, networks ...string) graph.Service {
	return graph.Service{
		Name:     name,
		Bindings: []graph.Binding{{HostIP: "127.0.0.1", Published: "8080", Target: 80}},
		Networks: networks,
	}
}

func findingsForRule(findings []report.Finding, ruleID string) []report.Finding {
	return slices.DeleteFunc(slices.Clone(findings), func(finding report.Finding) bool {
		return !strings.EqualFold(finding.RuleID, ruleID)
	})
}
