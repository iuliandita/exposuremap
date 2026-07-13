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

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/iuliandita/exposuremap/internal/graph"
)

const maxComposeBytes = 1 << 20

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

	model, err := loader.LoadModelWithContext(ctx, details, options)
	if err != nil {
		return graph.Revision{}, incomplete("parse compose file: invalid or unresolved Compose model")
	}
	if reason := unsupportedConstruct(model); reason != "" {
		return graph.Revision{}, incomplete("unsupported Compose construct: %s", reason)
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
