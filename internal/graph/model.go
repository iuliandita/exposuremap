package graph

import (
	"cmp"
	"net"
	"path"
	"slices"
	"strings"
)

// Reachability describes only what static configuration directly establishes.
type Reachability string

const (
	ReachabilityHostExposed Reachability = "host-exposed"
	ReachabilityInternal    Reachability = "internal"
	ReachabilityUnknown     Reachability = "unknown"
)

// Revision is the normalized graph for one configuration revision.
type Revision struct {
	Complete bool      `json:"complete"`
	Services []Service `json:"services"`
}

// Service contains the boundary-relevant facts observed for one service.
type Service struct {
	Name         string       `json:"name"`
	Image        string       `json:"image,omitempty"`
	BaseImage    string       `json:"base_image,omitempty"`
	Bindings     []Binding    `json:"bindings,omitempty"`
	Networks     []string     `json:"networks,omitempty"`
	Mounts       []Mount      `json:"mounts,omitempty"`
	Privileged   bool         `json:"privileged,omitempty"`
	HostNetwork  bool         `json:"host_network,omitempty"`
	Reachability Reachability `json:"reachability"`
}

// Binding is one published host-to-container port mapping.
type Binding struct {
	HostIP    string `json:"host_ip"`
	Published string `json:"published"`
	Target    uint32 `json:"target"`
	Protocol  string `json:"protocol"`
}

// Mount is one service mount. Sensitive is derived for bind mounts.
type Mount struct {
	Type      string `json:"type"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	ReadOnly  bool   `json:"read_only,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// Normalize returns a sorted graph without mutating the input.
func Normalize(revision Revision) Revision {
	result := Revision{
		Complete: revision.Complete,
		Services: make([]Service, 0, len(revision.Services)),
	}
	for _, service := range revision.Services {
		normalized := Service{
			Name:        service.Name,
			Image:       service.Image,
			BaseImage:   service.BaseImage,
			Bindings:    slices.Clone(service.Bindings),
			Networks:    sortedUnique(service.Networks),
			Mounts:      slices.Clone(service.Mounts),
			Privileged:  service.Privileged,
			HostNetwork: service.HostNetwork,
		}
		for index := range normalized.Bindings {
			normalized.Bindings[index].Protocol = strings.ToLower(normalized.Bindings[index].Protocol)
			if normalized.Bindings[index].Protocol == "" {
				normalized.Bindings[index].Protocol = "tcp"
			}
		}
		for index := range normalized.Mounts {
			normalized.Mounts[index].Type = strings.ToLower(normalized.Mounts[index].Type)
			normalized.Mounts[index].Sensitive = normalized.Mounts[index].Type == "bind" &&
				IsSensitiveHostPath(normalized.Mounts[index].Source)
		}
		slices.SortFunc(normalized.Bindings, compareBinding)
		slices.SortFunc(normalized.Mounts, compareMount)
		normalized.Reachability = reachability(normalized)
		result.Services = append(result.Services, normalized)
	}
	slices.SortFunc(result.Services, func(left, right Service) int {
		return cmp.Compare(left.Name, right.Name)
	})
	return result
}

// IsWildcardHost reports whether a binding observes an unspecified host address.
func IsWildcardHost(host string) bool {
	if host == "" {
		return true
	}
	address := net.ParseIP(strings.Trim(host, "[]"))
	return address != nil && address.IsUnspecified()
}

// IsLoopbackHost reports whether a binding observes an IP loopback address.
func IsLoopbackHost(host string) bool {
	address := net.ParseIP(strings.Trim(host, "[]"))
	return address != nil && address.IsLoopback()
}

// IsSensitiveHostPath reports whether a bind source directly exposes a sensitive host path.
func IsSensitiveHostPath(source string) bool {
	if !strings.HasPrefix(source, "/") {
		return false
	}
	cleaned := path.Clean(source)
	if cleaned == "/" || cleaned == "/run/docker.sock" || cleaned == "/var/run/docker.sock" {
		return true
	}
	for _, directory := range []string{"/etc", "/proc", "/sys"} {
		if cleaned == directory || strings.HasPrefix(cleaned, directory+"/") {
			return true
		}
	}
	return false
}

func reachability(service Service) Reachability {
	if service.HostNetwork || len(service.Bindings) > 0 {
		return ReachabilityHostExposed
	}
	if len(service.Networks) > 0 {
		return ReachabilityInternal
	}
	return ReachabilityUnknown
}

func sortedUnique(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}

func compareBinding(left, right Binding) int {
	if value := cmp.Compare(left.HostIP, right.HostIP); value != 0 {
		return value
	}
	if value := cmp.Compare(left.Published, right.Published); value != 0 {
		return value
	}
	if value := cmp.Compare(left.Target, right.Target); value != 0 {
		return value
	}
	return cmp.Compare(left.Protocol, right.Protocol)
}

func compareMount(left, right Mount) int {
	if value := cmp.Compare(left.Type, right.Type); value != 0 {
		return value
	}
	if value := cmp.Compare(left.Source, right.Source); value != 0 {
		return value
	}
	if value := cmp.Compare(left.Target, right.Target); value != 0 {
		return value
	}
	if left.ReadOnly == right.ReadOnly {
		return 0
	}
	if !left.ReadOnly {
		return -1
	}
	return 1
}
