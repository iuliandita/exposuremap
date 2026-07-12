package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestRunSuppressesDependencyLogs(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "base.yaml")
	head := filepath.Join(directory, "head.yaml")
	contents := []byte("services:\n  web:\n    image: ${UNSET_IMAGE}\n")
	for _, path := range []string{base, head} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	logger := logrus.StandardLogger()
	oldOutput := logger.Out
	t.Cleanup(func() { logger.SetOutput(oldOutput) })
	var dependencyLogs bytes.Buffer
	logger.SetOutput(&dependencyLogs)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"diff", "--base", base, "--head", head}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run() code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if dependencyLogs.Len() != 0 {
		t.Fatalf("dependency logs = %q, want empty", dependencyLogs.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
