package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iuliandita/exposuremap/internal/cli"
)

func TestRunDiffDetectsDataServicePathOnlyFromDockerfile(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", `services:
  web:
    image: nginx
    ports:
      - "127.0.0.1:8080:80"
    networks:
      - backend
networks:
  backend: {}
`)
	head := writeCLICompose(t, directory, "head.yaml", `services:
  web:
    image: nginx
    ports:
      - "127.0.0.1:8080:80"
    networks:
      - backend
  db:
    build: .
    image: example/custom-db:latest
    networks:
      - backend
networks:
  backend: {}
`)
	writeCLICompose(t, directory, "Dockerfile", "FROM postgres:16\n")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{`"rule_id": "EM-COMPOSE-005"`, "db (postgres:16)"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunDiffStaysQuietWhenReferencedDockerfileMissing(t *testing.T) {
	directory := t.TempDir()
	base := writeCLICompose(t, directory, "base.yaml", `services:
  web:
    image: nginx
    ports:
      - "127.0.0.1:8080:80"
    networks:
      - backend
networks:
  backend: {}
`)
	head := writeCLICompose(t, directory, "head.yaml", `services:
  web:
    image: nginx
    ports:
      - "127.0.0.1:8080:80"
    networks:
      - backend
  db:
    build: .
    image: example/custom-db:latest
    networks:
      - backend
networks:
  backend: {}
`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := cli.Run([]string{"diff", "--base", base, "--head", head, "--format", "json"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "EM-COMPOSE-005") {
		t.Fatalf("stdout unexpected EM-COMPOSE-005:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status": "warn"`) {
		t.Fatalf("stdout missing warning status:\n%s", stdout.String())
	}
}
