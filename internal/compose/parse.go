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
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/iuliandita/exposuremap/internal/graph"
)

const maxComposeBytes = 1 << 20

var boundaryFields = []string{"image", "ports", "networks", "volumes", "privileged", "network_mode"}

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
	symbolicVariables, field := boundaryInterpolation(model)
	if field != "" {
		return graph.Revision{}, incomplete("parse compose file: unresolved boundary interpolation in %s", field)
	}
	symbolicPorts, ok := allocateSymbolicPorts(model, content, symbolicVariables)
	if !ok {
		return graph.Revision{}, incomplete("parse compose file: too many symbolic published ports")
	}
	symbolicValues := make(map[string]string, len(symbolicPorts))
	for sentinel, expression := range symbolicPorts {
		variable, _ := symbolicVariable(expression)
		symbolicValues[variable] = sentinel
	}
	normalizedOptions := func(normalized *loader.Options) {
		options(normalized)
		normalized.Interpolate.LookupValue = func(variable string) (string, bool) {
			value, exists := symbolicValues[variable]
			return value, exists
		}
	}
	project, err := loader.LoadWithContext(ctx, details, normalizedOptions)
	if err != nil {
		return graph.Revision{}, incomplete("parse compose file: invalid or unresolved Compose model")
	}

	revision := graph.Revision{Complete: true, Services: make([]graph.Service, 0, len(project.Services))}
	for _, serviceName := range project.ServiceNames() {
		service := project.Services[serviceName]
		normalized := graph.Service{
			Name:        serviceName,
			Image:       service.Image,
			Privileged:  service.Privileged,
			HostNetwork: service.NetworkMode == "host",
		}
		for _, port := range service.Ports {
			published := port.Published
			if expression, exists := symbolicPorts[published]; exists {
				published = expression
			}
			normalized.Bindings = append(normalized.Bindings, graph.Binding{
				HostIP:    port.HostIP,
				Published: published,
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
		for _, field := range []string{"extends", "label_file"} {
			if _, exists := service[field]; exists {
				return fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
	}
	return ""
}

func boundaryInterpolation(model map[string]any) (map[string]struct{}, string) {
	symbolicVariables := map[string]struct{}{}
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
			if !exists {
				continue
			}
			unresolved := false
			if field == "ports" {
				unresolved = collectSymbolicPortVariables(value, symbolicVariables)
			} else {
				unresolved = containsUnresolvedBoundaryField(field, value)
			}
			if unresolved {
				return nil, fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
	}
	for _, serviceName := range serviceNames {
		service, _ := services[serviceName].(map[string]any)
		for _, field := range boundaryFields {
			value, exists := service[field]
			if !exists {
				continue
			}
			conflict := referencesSymbolicVariables(value, symbolicVariables)
			if field == "ports" {
				conflict = conflictingSymbolicPortVariable(value, symbolicVariables)
			}
			if conflict {
				return nil, fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
		for _, field := range []string{"env_file", "profiles"} {
			if value, exists := service[field]; exists && referencesSymbolicVariables(value, symbolicVariables) {
				return nil, fmt.Sprintf("services.%s.%s", serviceName, field)
			}
		}
	}
	return symbolicVariables, ""
}

func containsUnresolvedBoundaryField(field string, value any) bool {
	switch field {
	case "networks":
		return containsUnresolvedNetworkName(value)
	case "volumes":
		return containsUnresolvedSelectedFields(value, []string{"type", "source", "target", "read_only"})
	default:
		return containsUnresolvedInterpolation(value)
	}
}

func collectSymbolicPortVariables(value any, variables map[string]struct{}) bool {
	switch typed := value.(type) {
	case string:
		if !unresolvedInterpolation(typed) {
			return false
		}
		variable, ok := symbolicShortPortVariable(typed)
		if !ok {
			return true
		}
		variables[variable] = struct{}{}
	case []any:
		for _, item := range typed {
			if collectSymbolicPortVariables(item, variables) {
				return true
			}
		}
	case map[string]any:
		for _, field := range []string{"host_ip", "target", "protocol"} {
			if item, exists := typed[field]; exists && containsUnresolvedInterpolation(item) {
				return true
			}
		}
		published, exists := typed["published"]
		if !exists || !containsUnresolvedInterpolation(published) {
			return false
		}
		expression, ok := published.(string)
		if !ok {
			return true
		}
		variable, ok := symbolicVariable(expression)
		if !ok {
			return true
		}
		variables[variable] = struct{}{}
	}
	return false
}

func conflictingSymbolicPortVariable(value any, variables map[string]struct{}) bool {
	switch typed := value.(type) {
	case string:
		if variable, ok := symbolicShortPortVariable(typed); ok {
			typed = strings.Replace(typed, "${"+variable+"}", "65535", 1)
		}
		return referencesSymbolicVariables(typed, variables)
	case []any:
		for _, item := range typed {
			if conflictingSymbolicPortVariable(item, variables) {
				return true
			}
		}
	case map[string]any:
		for _, field := range []string{"host_ip", "target", "protocol"} {
			if item, exists := typed[field]; exists && referencesSymbolicVariables(item, variables) {
				return true
			}
		}
		published, exists := typed["published"]
		if !exists {
			return false
		}
		if expression, ok := published.(string); ok {
			if variable, symbolic := symbolicVariable(expression); symbolic {
				if _, accepted := variables[variable]; accepted {
					return false
				}
			}
		}
		return referencesSymbolicVariables(published, variables)
	}
	return false
}

func referencesSymbolicVariables(value any, variables map[string]struct{}) bool {
	for name := range template.ExtractVariables(map[string]any{"value": value}, nil) {
		if _, exists := variables[name]; exists {
			return true
		}
	}
	return false
}

func symbolicShortPortVariable(value string) (string, bool) {
	port := value
	if separator := strings.LastIndexByte(port, '/'); separator >= 0 {
		protocol := port[separator+1:]
		if protocol == "" || unresolvedInterpolation(protocol) {
			return "", false
		}
		port = port[:separator]
	}
	targetSeparator := strings.LastIndexByte(port, ':')
	if targetSeparator < 0 || !validPortNumber(port[targetSeparator+1:]) {
		return "", false
	}
	hostAndPublished := port[:targetSeparator]
	if variable, ok := symbolicVariable(hostAndPublished); ok {
		return variable, true
	}
	publishedSeparator := strings.LastIndexByte(hostAndPublished, ':')
	if publishedSeparator < 0 {
		return "", false
	}
	host := hostAndPublished[:publishedSeparator]
	if host == "" || unresolvedInterpolation(host) {
		return "", false
	}
	return symbolicVariable(hostAndPublished[publishedSeparator+1:])
}

func symbolicVariable(expression string) (string, bool) {
	if len(expression) < 4 || !strings.HasPrefix(expression, "${") || expression[len(expression)-1] != '}' {
		return "", false
	}
	name := expression[2 : len(expression)-1]
	for index := range len(name) {
		character := name[index]
		if index == 0 {
			if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
				return "", false
			}
			continue
		}
		if character != '_' && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return "", false
		}
	}
	return name, true
}

func validPortNumber(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port >= 1 && port <= 65535 && strconv.Itoa(port) == value
}

func allocateSymbolicPorts(
	model map[string]any,
	content []byte,
	variables map[string]struct{},
) (map[string]string, bool) {
	used := decimalPorts(content)
	collectConcretePublishedPorts(model, used)
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make(map[string]string, len(names))
	candidate := 65535
	for _, name := range names {
		allocated := false
		for candidate > 0 {
			value := strconv.Itoa(candidate)
			candidate--
			if _, exists := used[value]; exists {
				continue
			}
			result[value] = "${" + name + "}"
			allocated = true
			break
		}
		if !allocated {
			return nil, false
		}
	}
	return result, true
}

func decimalPorts(content []byte) map[string]struct{} {
	result := map[string]struct{}{}
	for start := 0; start < len(content); {
		if content[start] < '0' || content[start] > '9' {
			start++
			continue
		}
		end := start + 1
		for end < len(content) && content[end] >= '0' && content[end] <= '9' {
			end++
		}
		if value, err := strconv.Atoi(string(content[start:end])); err == nil && value >= 1 && value <= 65535 {
			result[strconv.Itoa(value)] = struct{}{}
		}
		start = end
	}
	return result
}

func collectConcretePublishedPorts(model map[string]any, used map[string]struct{}) {
	services, _ := model["services"].(map[string]any)
	for _, service := range services {
		fields, _ := service.(map[string]any)
		ports, _ := fields["ports"].([]any)
		for _, item := range ports {
			switch typed := item.(type) {
			case string:
				if _, symbolic := symbolicShortPortVariable(typed); symbolic {
					continue
				}
				resolved, err := substituteWithoutEnvironment(typed)
				if err != nil {
					continue
				}
				configs, err := types.ParsePortConfig(resolved)
				if err != nil {
					continue
				}
				for _, config := range configs {
					if config.Published != "" {
						used[config.Published] = struct{}{}
					}
				}
			case map[string]any:
				published, ok := typed["published"].(string)
				if !ok {
					continue
				}
				if _, symbolic := symbolicVariable(published); symbolic {
					continue
				}
				resolved, err := substituteWithoutEnvironment(published)
				if err == nil && resolved != "" {
					used[resolved] = struct{}{}
				}
			}
		}
	}
}

func substituteWithoutEnvironment(value string) (string, error) {
	return template.SubstituteWithOptions(
		value,
		func(string) (string, bool) { return "", false },
		template.WithoutLogging,
	)
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
