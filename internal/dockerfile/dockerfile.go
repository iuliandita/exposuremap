// Package dockerfile models the stage graph of a Dockerfile exactly or not at all.
package dockerfile

import (
	"regexp"
	"strings"
)

type stage struct {
	name  string
	image string
	exact bool
}

// Document is the parsed stage model of one Dockerfile.
type Document struct {
	stages    []stage
	defaults  map[string]string
	supported bool
}

var heredocPattern = regexp.MustCompile(`<<-?\s*["']?[A-Za-z_][A-Za-z0-9_]*["']?`)

// Parse lexes one Dockerfile into a stage model. Unsupported constructs mark the
// whole document unsupported instead of producing approximate facts.
func Parse(content []byte) Document {
	document := Document{defaults: map[string]string{}, supported: true}
	lines := logicalLines(string(content))
	seenFrom := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if heredocPattern.MatchString(trimmed) {
			document.supported = false
			continue
		}
		keyword, rest := splitKeyword(trimmed)
		switch strings.ToUpper(keyword) {
		case "FROM":
			seenFrom = true
			document.stages = append(document.stages, parseFrom(rest))
		case "ARG":
			if seenFrom {
				continue
			}
			parseArg(rest, document.defaults)
		}
	}
	return document
}

// FinalImage resolves the image identity of the final stage, or of the stage
// named by target. It returns false whenever resolution is not exact.
func (document Document) FinalImage(target string, args map[string]string) (string, bool) {
	if !document.supported || len(document.stages) == 0 {
		return "", false
	}
	index := len(document.stages) - 1
	if target != "" {
		index = document.lookup(target)
		if index < 0 {
			return "", false
		}
	}
	return document.chase(index, map[int]bool{}, args)
}

func (document Document) chase(index int, visited map[int]bool, args map[string]string) (string, bool) {
	if visited[index] {
		return "", false
	}
	visited[index] = true
	current := document.stages[index]
	if !current.exact {
		return "", false
	}
	image, ok := expandReference(current.image, args, document.defaults)
	if !ok || image == "" || strings.EqualFold(image, "scratch") {
		return "", false
	}
	if next := document.lookup(image); next >= 0 {
		return document.chase(next, visited, args)
	}
	return image, true
}

func (document Document) lookup(name string) int {
	for index, candidate := range document.stages {
		if candidate.name != "" && candidate.name == strings.ToLower(name) {
			return index
		}
	}
	return -1
}

func splitKeyword(line string) (string, string) {
	for index := 0; index < len(line); index++ {
		if line[index] == ' ' || line[index] == '\t' {
			return line[:index], strings.TrimLeft(line[index:], " \t")
		}
	}
	return line, ""
}

func parseFrom(rest string) stage {
	fields := strings.Fields(rest)
	images := make([]string, 0, 1)
	alias := ""
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		if strings.HasPrefix(field, "--") {
			continue
		}
		images = append(images, field)
		if index+1 < len(fields) && strings.EqualFold(fields[index+1], "as") {
			if index+3 != len(fields) {
				return stage{exact: false}
			}
			alias = strings.ToLower(fields[index+2])
			if strings.Contains(alias, "$") {
				return stage{exact: false}
			}
			break
		}
		if index+1 < len(fields) {
			return stage{exact: false}
		}
	}
	if len(images) != 1 {
		return stage{exact: false}
	}
	image := images[0]
	if strings.ContainsAny(image, `"'`) {
		return stage{exact: false}
	}
	return stage{name: alias, image: image, exact: true}
}

func parseArg(rest string, defaults map[string]string) {
	fields := strings.Fields(rest)
	if len(fields) != 1 {
		return
	}
	name, value, hasValue := strings.Cut(fields[0], "=")
	if !validVariableName(name) {
		return
	}
	if !hasValue {
		return
	}
	if strings.Contains(value, "$") {
		delete(defaults, name)
		return
	}
	defaults[name] = value
}

func logicalLines(content string) []string {
	raw := strings.Split(content, "\n")
	escape := byte('\\')
	directivesEnded := false
	lines := make([]string, 0, len(raw))
	var pending strings.Builder
	continuing := false
	for _, line := range raw {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if !directivesEnded && directiveLine(trimmed) {
			if key, value, ok := directive(trimmed); ok && key == "escape" {
				if value == "`" {
					return nil
				}
				escape = '\\'
			}
			continue
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			directivesEnded = true
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !continuing && trimmed == "" {
			continue
		}
		body := line
		continues := trailingEscape(body, escape)
		if continues {
			body = body[:len(body)-1]
		}
		pending.WriteString(body)
		if continues {
			continuing = true
			continue
		}
		lines = append(lines, pending.String())
		pending.Reset()
		continuing = false
	}
	if pending.Len() > 0 {
		lines = append(lines, pending.String())
	}
	return lines
}

func directiveLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	_, _, ok := directive(trimmed)
	return ok
}

func directive(trimmed string) (string, string, bool) {
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	key, value, found := strings.Cut(body, "=")
	if !found {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return "", "", false
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		if character < 'a' || character > 'z' {
			return "", "", false
		}
	}
	return key, strings.TrimSpace(value), true
}

func trailingEscape(line string, escape byte) bool {
	count := 0
	for index := len(line) - 1; index >= 0 && line[index] == escape; index-- {
		count++
	}
	return count%2 == 1
}

func expandReference(expression string, args, defaults map[string]string) (string, bool) {
	var result strings.Builder
	for index := 0; index < len(expression); {
		if expression[index] != '$' {
			result.WriteByte(expression[index])
			index++
			continue
		}
		if index+1 >= len(expression) {
			return "", false
		}
		if expression[index+1] == '$' {
			return "", false
		}
		if expression[index+1] == '{' {
			end := strings.IndexByte(expression[index+2:], '}')
			if end < 0 {
				return "", false
			}
			body := expression[index+2 : index+2+end]
			value, ok := expandBraced(body, args, defaults)
			if !ok {
				return "", false
			}
			result.WriteString(value)
			index += end + 3
			continue
		}
		name, length := variableName(expression[index+1:])
		if length == 0 {
			return "", false
		}
		value, ok := resolveVariable(name, args, defaults)
		if !ok {
			return "", false
		}
		result.WriteString(value)
		index += length + 1
	}
	return result.String(), true
}

func expandBraced(body string, args, defaults map[string]string) (string, bool) {
	for _, separator := range []string{":-", ":+", "-", "+"} {
		if position := strings.Index(body, separator); position > 0 {
			name := body[:position]
			word := body[position+len(separator):]
			if !validVariableName(name) || strings.Contains(word, "$") {
				return "", false
			}
			value, set := resolveVariableValue(name, args, defaults)
			switch separator {
			case ":-":
				if set && value != "" {
					return value, true
				}
				return word, true
			case "-":
				if set {
					return value, true
				}
				return word, true
			case ":+":
				if set && value != "" {
					return word, true
				}
				return "", true
			case "+":
				if set {
					return word, true
				}
				return "", true
			}
		}
	}
	if !validVariableName(body) {
		return "", false
	}
	return resolveVariable(body, args, defaults)
}

func resolveVariable(name string, args, defaults map[string]string) (string, bool) {
	value, ok := resolveVariableValue(name, args, defaults)
	return value, ok
}

func resolveVariableValue(name string, args, defaults map[string]string) (string, bool) {
	if value, exists := args[name]; exists {
		return value, true
	}
	if value, exists := defaults[name]; exists {
		return value, true
	}
	return "", false
}

func variableName(value string) (string, int) {
	for index := 0; index < len(value); index++ {
		character := value[index]
		valid := character == '_' ||
			(character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(index > 0 && character >= '0' && character <= '9')
		if !valid {
			return value[:index], index
		}
	}
	return value, len(value)
}

func validVariableName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	if first != '_' && (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if character != '_' && (character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
