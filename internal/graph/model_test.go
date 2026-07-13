package graph_test

import (
	"slices"
	"testing"

	"github.com/iuliandita/exposuremap/internal/graph"
)

func TestNormalizeSortsAndClassifiesServices(t *testing.T) {
	revision := graph.Normalize(graph.Revision{
		Complete: true,
		Services: []graph.Service{
			{
				Name:     "worker",
				Image:    "registry.example/team/Postgres:16-alpine@sha256:abc",
				Networks: []string{"queue", "backend", "queue"},
				Mounts: []graph.Mount{
					{Type: "bind", Source: "/var/run/docker.sock", Target: "/socket"},
					{Type: "volume", Source: "data", Target: "/data"},
				},
			},
			{
				Name: "web",
				Bindings: []graph.Binding{
					{HostIP: "127.0.0.1", Published: "9090", Target: 90, Protocol: "UDP"},
					{HostIP: "", Published: "8080", Target: 80},
				},
			},
			{Name: "host", HostNetwork: true},
			{Name: "orphan"},
		},
	})

	wantNames := []string{"host", "orphan", "web", "worker"}
	gotNames := make([]string, 0, len(revision.Services))
	for _, service := range revision.Services {
		gotNames = append(gotNames, service.Name)
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("service names = %#v, want %#v", gotNames, wantNames)
	}
	if revision.Services[0].Reachability != graph.ReachabilityHostExposed {
		t.Fatalf("host reachability = %q, want host-exposed", revision.Services[0].Reachability)
	}
	if revision.Services[1].Reachability != graph.ReachabilityUnknown {
		t.Fatalf("orphan reachability = %q, want unknown", revision.Services[1].Reachability)
	}
	if revision.Services[2].Reachability != graph.ReachabilityHostExposed {
		t.Fatalf("web reachability = %q, want host-exposed", revision.Services[2].Reachability)
	}
	if revision.Services[3].Reachability != graph.ReachabilityInternal {
		t.Fatalf("worker reachability = %q, want internal", revision.Services[3].Reachability)
	}
	if got, want := revision.Services[3].Image, "registry.example/team/Postgres:16-alpine@sha256:abc"; got != want {
		t.Fatalf("worker image = %q, want %q", got, want)
	}

	bindings := revision.Services[2].Bindings
	if len(bindings) != 2 || bindings[0].Published != "8080" || bindings[0].Protocol != "tcp" || bindings[1].Protocol != "udp" {
		t.Fatalf("normalized bindings = %#v", bindings)
	}
	if got := revision.Services[3].Networks; !slices.Equal(got, []string{"backend", "queue"}) {
		t.Fatalf("networks = %#v, want sorted unique networks", got)
	}
	if !revision.Services[3].Mounts[0].Sensitive {
		t.Fatalf("docker socket mount = %#v, want sensitive", revision.Services[3].Mounts[0])
	}
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	input := graph.Revision{Services: []graph.Service{{
		Name:     "web",
		Image:    "registry.example/team/Postgres:16-alpine@sha256:abc",
		Networks: []string{"z", "a"},
	}}}

	normalized := graph.Normalize(input)

	if !slices.Equal(input.Services[0].Networks, []string{"z", "a"}) {
		t.Fatalf("Normalize mutated input networks: %#v", input.Services[0].Networks)
	}
	if got, want := input.Services[0].Image, "registry.example/team/Postgres:16-alpine@sha256:abc"; got != want {
		t.Fatalf("Normalize mutated input image: %q, want %q", got, want)
	}
	if got, want := normalized.Services[0].Image, input.Services[0].Image; got != want {
		t.Fatalf("normalized image = %q, want %q", got, want)
	}
}

func TestHostAddressClasses(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wildcard bool
		loopback bool
	}{
		{name: "implicit wildcard", host: "", wildcard: true},
		{name: "IPv4 wildcard", host: "0.0.0.0", wildcard: true},
		{name: "IPv6 wildcard", host: "::", wildcard: true},
		{name: "IPv4 loopback", host: "127.42.0.1", loopback: true},
		{name: "IPv6 loopback", host: "::1", loopback: true},
		{name: "specific address", host: "192.0.2.10"},
		{name: "hostname is unknown", host: "localhost"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := graph.IsWildcardHost(test.host); got != test.wildcard {
				t.Fatalf("IsWildcardHost(%q) = %v, want %v", test.host, got, test.wildcard)
			}
			if got := graph.IsLoopbackHost(test.host); got != test.loopback {
				t.Fatalf("IsLoopbackHost(%q) = %v, want %v", test.host, got, test.loopback)
			}
		})
	}
}

func TestSensitiveHostPathsRespectPathBoundaries(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/", want: true},
		{path: "/etc", want: true},
		{path: "/etc/ssl", want: true},
		{path: "/sys/kernel", want: true},
		{path: "/var/run/docker.sock", want: true},
		{path: "/run/docker.sock", want: true},
		{path: "/etcetera", want: false},
		{path: "/srv/data", want: false},
		{path: "./relative", want: false},
	}

	for _, test := range tests {
		if got := graph.IsSensitiveHostPath(test.path); got != test.want {
			t.Errorf("IsSensitiveHostPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}
