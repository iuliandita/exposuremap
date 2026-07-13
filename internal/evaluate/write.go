package evaluate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iuliandita/exposuremap/internal/report"
)

// WriteBundle atomically publishes a benchmark run without replacing existing output.
func WriteBundle(output string, bundle Bundle) (returnError error) {
	if output == "" {
		return fmt.Errorf("output directory is required")
	}
	output = filepath.Clean(output)
	base := filepath.Base(output)
	if base == "." || base == string(filepath.Separator) {
		return fmt.Errorf("output directory must name a new child directory")
	}
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("output directory already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create output parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, "."+base+"-")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return fmt.Errorf("set temporary output permissions: %w", err)
	}
	defer func() {
		if temporary != "" {
			if err := os.RemoveAll(temporary); returnError == nil && err != nil {
				returnError = fmt.Errorf("remove temporary output: %w", err)
			}
		}
	}()

	reportsDirectory := filepath.Join(temporary, "reports")
	if err := os.Mkdir(reportsDirectory, 0o700); err != nil {
		return fmt.Errorf("create reports directory: %w", err)
	}
	if err := writeJSONFile(filepath.Join(temporary, "results.json"), bundle.Results); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(temporary, "summary.json"), bundle.Summary); err != nil {
		return err
	}
	for _, caseReport := range bundle.Reports {
		if !safeArtifactName(caseReport.Case.ID) {
			return fmt.Errorf("unsafe report case id %q", caseReport.Case.ID)
		}
		if err := writeJSONFile(filepath.Join(reportsDirectory, caseReport.Case.ID+".json"), caseReport); err != nil {
			return err
		}
		var markdown strings.Builder
		if err := report.RenderMarkdown(&markdown, caseReport.Analyzer); err != nil {
			return fmt.Errorf("render report %s: %w", caseReport.Case.ID, err)
		}
		if err := writeFile(filepath.Join(reportsDirectory, caseReport.Case.ID+".md"), strings.NewReader(markdown.String())); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, output); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	temporary = ""
	return nil
}

func writeJSONFile(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	encoded = append(encoded, '\n')
	return writeFile(path, strings.NewReader(string(encoded)))
}

func writeFile(path string, content io.Reader) (returnError error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer func() {
		if err := file.Close(); returnError == nil && err != nil {
			returnError = fmt.Errorf("close %s: %w", filepath.Base(path), err)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set %s permissions: %w", filepath.Base(path), err)
	}
	if _, err := io.Copy(file, content); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Base(path), err)
	}
	return nil
}

func safeArtifactName(value string) bool {
	if value == "" || value == "." || value == ".." || strings.HasPrefix(value, ".") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
