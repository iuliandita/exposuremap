// Package analyze compares normalized revisions using deterministic rules.
package analyze

import (
	"fmt"
	"strings"

	"github.com/iuliandita/exposuremap/internal/graph"
	"github.com/iuliandita/exposuremap/internal/report"
)

const (
	// RulePackVersion identifies the deterministic Compose boundary rules.
	RulePackVersion = "compose-v2"

	ruleNewHostPort   = "EM-COMPOSE-001"
	ruleBindWidened   = "EM-COMPOSE-002"
	ruleHostNetwork   = "EM-COMPOSE-003"
	ruleSensitiveHost = "EM-COMPOSE-004"
	ruleDataService   = "EM-COMPOSE-005"
)

// Compare reports widening changes from base to head.
func Compare(base, head graph.Revision) []report.Finding {
	baseServices := serviceIndex(base)
	baseExposures := hostExposureSet(base)
	headExposures := hostExposureSet(head)
	findings := make([]report.Finding, 0)
	for _, headService := range head.Services {
		baseService := baseServices[headService.Name]
		findings = append(findings, compareBindings(baseService, headService, baseExposures, headExposures)...)
		if headService.HostNetwork && !baseService.HostNetwork {
			findings = append(findings, report.Finding{
				RuleID:      ruleHostNetwork,
				Title:       "Host network mode introduced",
				Severity:    "high",
				Confidence:  "high",
				Service:     headService.Name,
				Evidence:    "network_mode: <none> -> host",
				Base:        "service did not use the host network namespace",
				Head:        "service uses the host network namespace",
				Remediation: "Use an isolated Compose network and publish only required ports.",
			})
		}
		if headService.Reachability == graph.ReachabilityHostExposed {
			findings = append(findings, compareSensitiveCapabilities(baseService, headService)...)
		}
	}
	findings = append(findings, compareDataServicePaths(base, head)...)
	return report.SortFindings(findings)
}

func compareBindings(
	base, head graph.Service,
	baseExposures, headExposures map[string]struct{},
) []report.Finding {
	baseBySocket := make(map[string][]graph.Binding, len(base.Bindings))
	for _, binding := range base.Bindings {
		key := socketKey(binding)
		baseBySocket[key] = append(baseBySocket[key], binding)
	}
	result := make([]report.Finding, 0)
	emittedWidening := make(map[string]struct{})
	emittedPort := make(map[string]struct{})
	consumedReplacements := make(map[string]struct{})
	for _, binding := range head.Bindings {
		key := socketKey(binding)
		baseBindings := baseBySocket[key]
		if len(baseBindings) == 0 {
			if _, exists := baseExposures[hostExposureKey(binding)]; exists {
				continue
			}
			if replacedBinding(base.Bindings, binding, headExposures, consumedReplacements) {
				continue
			}
			if _, exists := emittedPort[key]; exists {
				continue
			}
			emittedPort[key] = struct{}{}
			result = append(result, report.Finding{
				RuleID:      ruleNewHostPort,
				Title:       "New host port published",
				Severity:    "medium",
				Confidence:  "high",
				Service:     head.Name,
				Evidence:    bindingLabel(binding),
				Base:        "binding absent",
				Head:        bindingLabel(binding),
				Remediation: "Remove the binding or restrict it to the narrowest required host address.",
			})
			continue
		}
		if !graph.IsWildcardHost(binding.HostIP) {
			continue
		}
		if containsWildcard(baseBindings) {
			continue
		}
		for _, baseBinding := range baseBindings {
			if !graph.IsLoopbackHost(baseBinding.HostIP) {
				continue
			}
			if _, exists := emittedWidening[key]; exists {
				break
			}
			emittedWidening[key] = struct{}{}
			result = append(result, report.Finding{
				RuleID:      ruleBindWidened,
				Title:       "Host binding widened from loopback",
				Severity:    "high",
				Confidence:  "high",
				Service:     head.Name,
				Evidence:    bindingLabel(baseBinding) + " -> " + bindingLabel(binding),
				Base:        bindingLabel(baseBinding),
				Head:        bindingLabel(binding),
				Remediation: "Restore a loopback host address unless non-local access is intentional.",
			})
			break
		}
	}
	return result
}

func replacedBinding(
	baseBindings []graph.Binding,
	headBinding graph.Binding,
	headExposures map[string]struct{},
	consumed map[string]struct{},
) bool {
	for _, baseBinding := range baseBindings {
		if baseBinding.Target != headBinding.Target || baseBinding.Protocol != headBinding.Protocol ||
			hostScope(baseBinding.HostIP) != hostScope(headBinding.HostIP) {
			continue
		}
		key := hostExposureKey(baseBinding)
		if _, stillExposed := headExposures[key]; stillExposed {
			continue
		}
		if _, used := consumed[key]; used {
			continue
		}
		consumed[key] = struct{}{}
		return true
	}
	return false
}

func containsWildcard(bindings []graph.Binding) bool {
	for _, binding := range bindings {
		if graph.IsWildcardHost(binding.HostIP) {
			return true
		}
	}
	return false
}

func compareSensitiveCapabilities(base, head graph.Service) []report.Finding {
	result := make([]report.Finding, 0)
	if head.Privileged && !base.Privileged {
		result = append(result, report.Finding{
			RuleID:      ruleSensitiveHost,
			Title:       "Host-exposed service became privileged",
			Severity:    "high",
			Confidence:  "high",
			Service:     head.Name,
			Evidence:    "privileged: false -> true",
			Base:        "privileged mode disabled",
			Head:        "privileged mode enabled",
			Remediation: "Remove privileged mode and grant only the specific capabilities required.",
		})
	}
	baseMounts := make(map[string]struct{}, len(base.Mounts))
	for _, mount := range base.Mounts {
		baseMounts[mountKey(mount)] = struct{}{}
	}
	for _, mount := range head.Mounts {
		if !mount.Sensitive {
			continue
		}
		if _, exists := baseMounts[mountKey(mount)]; exists {
			continue
		}
		severity := "medium"
		if mount.Source == "/" || strings.HasSuffix(mount.Source, "/docker.sock") {
			severity = "high"
		}
		label := fmt.Sprintf("mount %s -> %s", mount.Source, mount.Target)
		result = append(result, report.Finding{
			RuleID:      ruleSensitiveHost,
			Title:       "Host-exposed service gained a sensitive mount",
			Severity:    severity,
			Confidence:  "high",
			Service:     head.Name,
			Evidence:    label,
			Base:        "mount absent",
			Head:        label,
			Remediation: "Remove the host bind or expose the smallest read-only path required.",
		})
	}
	return result
}

func serviceIndex(revision graph.Revision) map[string]graph.Service {
	result := make(map[string]graph.Service, len(revision.Services))
	for _, service := range revision.Services {
		result[service.Name] = service
	}
	return result
}

func hostExposureSet(revision graph.Revision) map[string]struct{} {
	result := make(map[string]struct{})
	for _, service := range revision.Services {
		for _, binding := range service.Bindings {
			result[hostExposureKey(binding)] = struct{}{}
		}
	}
	return result
}

func hostExposureKey(binding graph.Binding) string {
	return fmt.Sprintf("%s|%s|%s", hostScope(binding.HostIP), binding.Published, binding.Protocol)
}

func hostScope(host string) string {
	if graph.IsWildcardHost(host) {
		return "wildcard"
	}
	if graph.IsLoopbackHost(host) {
		return "loopback"
	}
	return "address:" + strings.Trim(host, "[]")
}

func socketKey(binding graph.Binding) string {
	return fmt.Sprintf("%s|%s|%d", binding.Protocol, binding.Published, binding.Target)
}

func mountKey(mount graph.Mount) string {
	return fmt.Sprintf("%s|%s|%s", mount.Type, mount.Source, mount.Target)
}

func bindingLabel(binding graph.Binding) string {
	host := binding.HostIP
	if graph.IsWildcardHost(host) {
		host = "*"
	}
	published := binding.Published
	if published == "" {
		published = "ephemeral"
	}
	return fmt.Sprintf("%s:%s->%d/%s", host, published, binding.Target, binding.Protocol)
}
