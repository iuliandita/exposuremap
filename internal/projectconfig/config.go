// Package projectconfig loads explicit, bounded ExposureMap context files.
package projectconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v4"
)

const (
	maxContextBytes = 64 << 10
	maxVariables    = 256
	maxValueBytes   = 4096
)

// Variables contains the explicit interpolation values for each revision.
type Variables struct {
	Base map[string]string
	Head map[string]string
}

type document struct {
	SchemaVersion int            `yaml:"schema_version"`
	Variables     variableGroups `yaml:"variables"`
}

type variableGroups struct {
	Common variableMap `yaml:"common"`
	Base   variableMap `yaml:"base"`
	Head   variableMap `yaml:"head"`
}

type variableMap map[string]strictString

type strictString string

func (value *strictString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!str" {
		return errors.New("context variable values must be strings")
	}
	*value = strictString(node.Value)
	return nil
}

// Load reads and validates one explicitly selected context file.
func Load(path string) (Variables, error) {
	content, err := readRegularFile(path)
	if err != nil {
		return Variables{}, fmt.Errorf("read context file: %s", safeFileError(err))
	}

	var parsed document
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(&parsed); err != nil {
		return Variables{}, errors.New("invalid context document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Variables{}, errors.New("invalid context document")
	}
	if parsed.SchemaVersion != 1 {
		return Variables{}, errors.New("context schema_version must be 1")
	}
	if err := validateVariables(parsed.Variables); err != nil {
		return Variables{}, err
	}

	base := clone(parsed.Variables.Common)
	head := clone(parsed.Variables.Common)
	merge(base, parsed.Variables.Base)
	merge(head, parsed.Variables.Head)
	return Variables{Base: base, Head: head}, nil
}

func validateVariables(groups variableGroups) error {
	names := make(map[string]struct{})
	for _, variables := range []variableMap{groups.Common, groups.Base, groups.Head} {
		for name, value := range variables {
			if !validVariableName(name) {
				return errors.New("context contains an invalid variable name")
			}
			if len(value) > maxValueBytes {
				return errors.New("context variable value exceeds 4096 bytes")
			}
			names[name] = struct{}{}
		}
	}
	if len(names) > maxVariables {
		return errors.New("context contains more than 256 variables")
	}
	return nil
}

func validVariableName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		character := name[index]
		if index == 0 {
			if character != '_' && (character < 'A' || character > 'Z') &&
				(character < 'a' || character > 'z') {
				return false
			}
			continue
		}
		if character != '_' && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func clone(source variableMap) map[string]string {
	result := make(map[string]string, len(source))
	merge(result, source)
	return result
}

func merge(target map[string]string, source variableMap) {
	for name, value := range source {
		target[name] = string(value)
	}
}

func readRegularFile(path string) (returnContent []byte, returnError error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("input is not a regular file")
	}
	if info.Size() > maxContextBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxContextBytes)
	}
	file, err := os.Open(path)
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
		return nil, errors.New("input changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxContextBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxContextBytes {
		return nil, fmt.Errorf("input exceeds %d bytes", maxContextBytes)
	}
	return content, nil
}

func safeFileError(err error) string {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return pathError.Op + ": " + pathError.Err.Error()
	}
	return err.Error()
}
