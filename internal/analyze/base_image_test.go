package analyze

import (
	"testing"

	"github.com/iuliandita/exposuremap/internal/graph"
)

func TestCompareDetectsDataServicePathFromBaseImage(t *testing.T) {
	source := exposedService("web", "backend")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{
			Name:      "db",
			Image:     "example/custom-db:latest",
			BaseImage: "postgres:16",
			Networks:  []string{"backend"},
		},
	)

	findings := findingsForRule(Compare(base, head).Findings, "EM-COMPOSE-005")
	if len(findings) != 1 {
		t.Fatalf("rule 005 findings = %#v, want one", findings)
	}
	if want := "web -> backend -> db (postgres:16)"; findings[0].Evidence != want {
		t.Fatalf("evidence = %q, want %q", findings[0].Evidence, want)
	}
}

func TestComparePrefersRecognizedComposeImageForEvidence(t *testing.T) {
	source := exposedService("web", "backend")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{
			Name:      "db",
			Image:     "mysql:8",
			BaseImage: "postgres:16",
			Networks:  []string{"backend"},
		},
	)

	findings := findingsForRule(Compare(base, head).Findings, "EM-COMPOSE-005")
	if len(findings) != 1 {
		t.Fatalf("rule 005 findings = %#v, want one", findings)
	}
	if want := "web -> backend -> db (mysql:8)"; findings[0].Evidence != want {
		t.Fatalf("evidence = %q, want %q", findings[0].Evidence, want)
	}
}

func TestCompareIgnoresUnrecognizedBaseImage(t *testing.T) {
	source := exposedService("web", "backend")
	base := normalizedRevision(source)
	head := normalizedRevision(
		source,
		graph.Service{
			Name:      "app",
			BaseImage: "alpine:3.20",
			Networks:  []string{"backend"},
		},
	)

	findings := findingsForRule(Compare(base, head).Findings, "EM-COMPOSE-005")
	if len(findings) != 0 {
		t.Fatalf("rule 005 findings = %#v, want none", findings)
	}
}

func TestPureServiceRenamesCompareBaseImage(t *testing.T) {
	base := normalizedRevision(graph.Service{
		Name:      "datastore",
		BaseImage: "postgres:16",
		Networks:  []string{"backend"},
	})
	head := normalizedRevision(graph.Service{
		Name:      "db",
		BaseImage: "postgres:16",
		Networks:  []string{"backend"},
	})
	if renamed := pureServiceRenames(base, head); renamed["datastore"] != "db" {
		t.Fatalf("renames = %#v, want datastore -> db", renamed)
	}

	changedBase := normalizedRevision(graph.Service{
		Name:      "datastore",
		BaseImage: "postgres:15",
		Networks:  []string{"backend"},
	})
	if renamed := pureServiceRenames(changedBase, head); len(renamed) != 0 {
		t.Fatalf("renames = %#v, want none for differing base images", renamed)
	}
}
