// Package compose translates one Compose file into the ExposureMap graph.
package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/iuliandita/exposuremap/internal/graph"
)

const maxComposeBytes = 1 << 20

var boundaryFields = []string{"ports", "networks", "volumes", "privileged", "network_mode"}

// IncompleteError means the input could not be represented safely and completely.
type IncompleteError struct {
	Reason string
}

func (err *IncompleteError) Error() string {
	return err.Reason
}

// ParseFile safely loads one regular Compose file and normalizes boundary facts.
func ParseFile(ctx context.Context, filePath string) (graph.Revision, error) {
	content, err := readRegularFile(filePath)
	if err != nil {
		return graph.Revision{}, incomplete("read compose file: %s", safeFileError(err))
	}
	details := types.ConfigDetails{
		WorkingDir: filepath.Dir(filePath),
		ConfigFiles: []types.ConfigFile{{
			Filename: filepath.Base(filePath),
			Content:  content,
		}},
		Environment: types.Mapping{},
	}
	options := func(options *loader.Options) {
		options.ResolvePaths = false
		options.SkipInclude = true
		options.SkipExtends = true
		options.SkipResolveEnvironment = true
		options.SetProjectName("exposuremap", true)
	}
	rawOptions := func(raw *loader.Options) {
		options(raw)
		raw.SkipInterpolation = true
		raw.SkipValidation = true
		raw.SkipConsistencyCheck = true
		raw.SkipDefaultValues = true
		raw.SkipNormalization = true
	}

	model, err := loader.LoadModelWithContext(ctx, details, rawOptions)
	if err != nil {
		return graph.Revision{}, incomplete("parse compose file: invalid or unresolved Compose model")
	}
	if reason := unsupportedConstruct(model); reason != "" {
		return graph.Revision{}, incomplete("unsupported Compose construct: %s", reason)
	}
	if field := unresolvedBoundaryField(model); field != "" {
		return graph.Revision{}, incomplete("parse compose file: unresolved boundary interpolation in %s", field)
	}
	project, err := loader.LoadWithContext(ctx, details, options)
	if err != nil {
		return graph.Revision{}, incomplete("parse compose file: invalid or unresolved Compose model")
	}

	revision := graph.Revision{Complete: true, Services: make([]graph.Service, 0, len(project.Services))}
	for _, serviceName := range project.ServiceNames() {
		service := project.Services[serviceName]
		normalized := graph.Service{
			Name:        serviceName,
			Privileged:  service.Privileged,
			HostNetwork: service.NetworkMode == "host",
		}
		for _, port := range service.Ports {
			normalized.Bindings = append(normalized.Bindings, graph.Binding{
				HostIP:    port.HostIP,
				Published: port.Published,
				Target:    port.Target,
				Protocol:  port.Protocol,
			})
		}
		for network := range service.Networks {
			normalized.Networks = append(normalized.Networks, network)
		}
		if len(normalized.Networks) == 0 && service.NetworkMode == "" {
			normalized.Networks = []string{"default"}
		}
		for _, mount := range service.Volumes {
			normalized.Mounts = append(normalized.Mounts, graph.Mount{
				Type:     mount.Type,
				Source:   mount.Source,
				Target:   mount.Target,
				ReadOnly: mount.ReadOnly,
			})
		}
		revision.Services = append(revision.Services, normalized)
	}
	return graph.Normalize(revision), nil
}

func readRegularFile(filePath string) (returnContent []byte, returnError error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("input is not a regular file")
	}
	if info.Size() > maxComposeBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxComposeBytes)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeError := file.Close(); returnError == nil && closeError != nil {
			returnError = closeError
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("input changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxComposeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxComposeBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxComposeBytes)
	}
	return content, nil
}

func unsupportedConstruct(model map[string]any) string {
	if _, exists := model["include"]; exists {
		return "include"
	}
	services, _ := model["services"].(map[string]any)
	serviceNames := make([]string, 0, len(services))
	for serviceName := range services {
		serviceNames = append(serviceNames, serviceName)
	}
	slices.Sort(serviceNames)
	for _, serviceName := range serviceNames {
		service, _ := services[serviceName].(map[string]any)
		for _, field := range []string{"extends", "label_file", "profiles"} {
			if _, exists := service[field]; exists {
				return fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
	}
	return ""
}

func unresolvedBoundaryField(model map[string]any) string {
	services, _ := model["services"].(map[string]any)
	serviceNames := make([]string, 0, len(services))
	for serviceName := range services {
		serviceNames = append(serviceNames, serviceName)
	}
	slices.Sort(serviceNames)
	for _, serviceName := range serviceNames {
		service, _ := services[serviceName].(map[string]any)
		for _, field := range boundaryFields {
			value, exists := service[field]
			if exists && containsUnresolvedBoundaryField(field, value) {
				return fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
	}
	return ""
}

func containsUnresolvedBoundaryField(field string, value any) bool {
	switch field {
	case "ports":
		return containsUnresolvedSelectedFields(value, []string{"host_ip", "published", "target", "protocol"})
	case "networks":
		return containsUnresolvedNetworkName(value)
	case "volumes":
		return containsUnresolvedSelectedFields(value, []string{"type", "source", "target", "read_only"})
	default:
		return containsUnresolvedInterpolation(value)
	}
}

func containsUnresolvedSelectedFields(value any, fields []string) bool {
	switch typed := value.(type) {
	case string:
		return unresolvedInterpolation(typed)
	case []any:
		for _, item := range typed {
			if containsUnresolvedSelectedFields(item, fields) {
				return true
			}
		}
	case map[string]any:
		for _, field := range fields {
			if item, exists := typed[field]; exists && containsUnresolvedInterpolation(item) {
				return true
			}
		}
	}
	return false
}

func containsUnresolvedNetworkName(value any) bool {
	switch typed := value.(type) {
	case string:
		return unresolvedInterpolation(typed)
	case []any:
		for _, item := range typed {
			if containsUnresolvedNetworkName(item) {
				return true
			}
		}
	case map[string]any:
		for network := range typed {
			if unresolvedInterpolation(network) {
				return true
			}
		}
	}
	return false
}

func containsUnresolvedInterpolation(value any) bool {
	switch typed := value.(type) {
	case string:
		return unresolvedInterpolation(typed)
	case []any:
		for _, item := range typed {
			if containsUnresolvedInterpolation(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if unresolvedInterpolation(key) || containsUnresolvedInterpolation(item) {
				return true
			}
		}
	}
	return false
}

func unresolvedInterpolation(value string) bool {
	if nestedInterpolation(value) {
		return true
	}
	_, err := template.SubstituteWithOptions(
		value,
		func(string) (string, bool) { return "", false },
		template.WithReplacementFunction(requireResolvedReplacement),
		template.WithoutLogging,
	)
	return err != nil
}

func requireResolvedReplacement(
	value string,
	mapping template.Mapping,
	config *template.Config,
) (string, error) {
	replacement, applied, err := template.DefaultReplacementAppliedFunc(value, mapping, config)
	if err != nil {
		return "", err
	}
	if !applied {
		return "", errors.New("unresolved interpolation")
	}
	return replacement, nil
}

func nestedInterpolation(value string) bool {
	for index := 0; index+1 < len(value); index++ {
		if value[index] != '$' {
			continue
		}
		if value[index+1] == '$' {
			index++
			continue
		}
		if value[index+1] != '{' {
			continue
		}
		for nested := index + 2; nested+1 < len(value); nested++ {
			if value[nested] == '}' {
				break
			}
			if value[nested] != '$' {
				continue
			}
			if value[nested+1] == '$' {
				nested++
				continue
			}
			if strings.HasPrefix(value[nested:], "${") {
				return true
			}
		}
	}
	return false
}

func incomplete(format string, arguments ...any) *IncompleteError {
	return &IncompleteError{Reason: fmt.Sprintf(format, arguments...)}
}

func safeFileError(err error) string {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Op + ": " + pathError.Err.Error()
	}
	return err.Error()
}
