package compose_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	composeparser "github.com/iuliandita/exposuremap/internal/compose"
	"github.com/iuliandita/exposuremap/internal/graph"
)

func TestParseFileNormalizesSupportedBoundaryFacts(t *testing.T) {
	revision, err := composeparser.ParseFile(context.Background(), "testdata/supported.yaml")
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
	if len(revision.Services) != 4 {
		t.Fatalf("services = %#v, want four", revision.Services)
	}

	database := serviceNamed(t, revision, "database")
	if database.Image != "postgres:18" {
		t.Fatalf("database image = %q, want postgres:18", database.Image)
	}
	if !slices.Equal(database.Networks, []string{"default"}) || database.Reachability != graph.ReachabilityInternal {
		t.Fatalf("database = %#v, want implicit internal default network", database)
	}
	host := serviceNamed(t, revision, "host")
	if !host.HostNetwork || host.Reachability != graph.ReachabilityHostExposed {
		t.Fatalf("host = %#v, want host-exposed host mode", host)
	}
	web := serviceNamed(t, revision, "web")
	if len(web.Bindings) != 2 {
		t.Fatalf("web bindings = %#v, want two", web.Bindings)
	}
	if web.Bindings[0].Published != "8080" || web.Bindings[0].Target != 80 || web.Bindings[0].Protocol != "tcp" {
		t.Fatalf("short binding = %#v", web.Bindings[0])
	}
	if web.Bindings[1].HostIP != "127.0.0.1" || web.Bindings[1].Published != "8443" || web.Bindings[1].Target != 443 {
		t.Fatalf("long binding = %#v", web.Bindings[1])
	}
	if len(web.Mounts) != 1 || !web.Mounts[0].Sensitive || !web.Mounts[0].ReadOnly {
		t.Fatalf("web mounts = %#v, want read-only sensitive bind", web.Mounts)
	}
	worker := serviceNamed(t, revision, "worker")
	if !worker.Privileged || len(worker.Mounts) != 1 || worker.Mounts[0].Type != "bind" {
		t.Fatalf("worker = %#v, want privileged with bind mount", worker)
	}
}

func TestParseFileRejectsNonRegularAndOversizedInputs(t *testing.T) {
	directory := t.TempDir()
	target := writeCompose(t, directory, "target.yaml", "services:\n  web:\n    image: nginx\n")
	symlink := filepath.Join(directory, "link.yaml")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	for name, candidate := range map[string]string{
		"directory": directory,
		"symlink":   symlink,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := composeparser.ParseFile(context.Background(), candidate)
			assertIncomplete(t, err)
		})
	}

	oversized := filepath.Join(directory, "oversized.yaml")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err := composeparser.ParseFile(context.Background(), oversized)
	assertIncomplete(t, err)
}

func TestParseFileRejectsUnsupportedExternalInputs(t *testing.T) {
	tests := map[string]string{
		"extends":    "services:\n  web:\n    extends:\n      file: other.yaml\n      service: base\n",
		"label file": "services:\n  web:\n    image: nginx\n    label_file: labels.env\n",
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			assertIncomplete(t, err)
		})
	}
}

func TestParseFilePreservesSymbolicPublishedPorts(t *testing.T) {
	t.Setenv("HTTP_PORT", "19080")
	t.Setenv("ADMIN_PORT", "19081")
	t.Setenv("HTTPS_PORT", "19443")
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - ${HTTP_PORT}:80
      - 127.0.0.1:${ADMIN_PORT}:8080/udp
      - target: 443
        published: ${HTTPS_PORT}
        protocol: tcp
      - 65535:81
      - 65534:82
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if !revision.Complete || web.Reachability != graph.ReachabilityHostExposed {
		t.Fatalf("revision = %#v, want complete host-exposed service", revision)
	}
	if len(web.Bindings) != 5 {
		t.Fatalf("bindings = %#v, want five", web.Bindings)
	}
	wants := map[uint32]graph.Binding{
		80:   {Published: "${HTTP_PORT}", Target: 80, Protocol: "tcp"},
		81:   {Published: "65535", Target: 81, Protocol: "tcp"},
		82:   {Published: "65534", Target: 82, Protocol: "tcp"},
		443:  {Published: "${HTTPS_PORT}", Target: 443, Protocol: "tcp"},
		8080: {HostIP: "127.0.0.1", Published: "${ADMIN_PORT}", Target: 8080, Protocol: "udp"},
	}
	for _, binding := range web.Bindings {
		if want, exists := wants[binding.Target]; !exists || binding != want {
			t.Fatalf("binding = %#v, want one of %#v", binding, wants)
		}
	}
}

func TestParseFileDoesNotRestoreInterpolatedConcretePortAsSymbolic(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - ${PORT}:80
      - ${STATIC:-6553}5:81
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if len(web.Bindings) != 2 {
		t.Fatalf("bindings = %#v, want two", web.Bindings)
	}
	wants := map[uint32]string{80: "${PORT}", 81: "65535"}
	for _, binding := range web.Bindings {
		if want := wants[binding.Target]; binding.Published != want {
			t.Fatalf("binding = %#v, want published %q", binding, want)
		}
	}
}

func TestParseFileUsesDefaultProfileProjection(t *testing.T) {
	t.Setenv("COMPOSE_PROFILES", "debug")
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  core:
    image: nginx
    ports:
      - 8080:80
  debug:
    image: nginx
    profiles: [debug]
    env_file: missing.env
    ports:
      - 9000:90
  metrics:
    image: nginx
    profiles: [all, metrics]
    ports:
      - 9100:91
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
	if got, want := len(revision.Services), 1; got != want {
		t.Fatalf("services = %#v, want %d default-profile service", revision.Services, want)
	}
	if got := revision.Services[0].Name; got != "core" {
		t.Fatalf("service name = %q, want core", got)
	}
}

func TestParseFileRejectsSymbolicPortVariableReuseInGraphFields(t *testing.T) {
	tests := map[string]struct {
		contents   string
		diagnostic string
	}{
		"image default": {
			contents:   "services:\n  web:\n    image: ${PORT:-nginx}\n    ports:\n      - ${PORT}:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.image",
		},
		"published default": {
			contents:   "services:\n  web:\n    image: nginx\n    ports:\n      - ${PORT}:80\n      - ${PORT:-8080}:81\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"profile default": {
			contents:   "services:\n  web:\n    image: nginx\n    profiles: [\"${PORT:-debug}\"]\n    ports:\n      - ${PORT}:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.profiles",
		},
		"required env file": {
			contents:   "services:\n  web:\n    image: nginx\n    env_file: ${PORT:?required}\n    ports:\n      - ${PORT}:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.env_file",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", test.contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			var incomplete *composeparser.IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want *compose.IncompleteError", err)
			}
			if got := incomplete.Error(); got != test.diagnostic {
				t.Fatalf("error = %q, want %q", got, test.diagnostic)
			}
		})
	}
}

func TestParseFileIgnoresServiceEnvFilesWithoutReadingThem(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  long:
    image: nginx
    env_file:
      - path: missing-long.env
        required: true
    ports:
      - "127.0.0.1:8081:80"
  short:
    image: nginx
    env_file: missing-short.env
    ports:
      - "127.0.0.1:8080:80"
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
	for serviceName, published := range map[string]string{"long": "8081", "short": "8080"} {
		service := serviceNamed(t, revision, serviceName)
		if len(service.Bindings) != 1 || service.Bindings[0].Published != published {
			t.Fatalf("%s bindings = %#v, want published port %s", serviceName, service.Bindings, published)
		}
	}
}

func TestParseFileRejectsMalformedOrUnresolvedInput(t *testing.T) {
	tests := map[string]string{
		"malformed":               "services: [",
		"unresolved env file":     "services:\n  web:\n    image: nginx\n    env_file: ${ENV_FILE:?required}\n",
		"unresolved publish port": "services:\n  web:\n    image: nginx\n    ports:\n      - ${PUBLISHED:?required}:80\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			assertIncomplete(t, err)
		})
	}
}

func TestParseFileRejectsUnresolvedBoundaryInterpolation(t *testing.T) {
	tests := map[string]struct {
		contents   string
		diagnostic string
	}{
		"short volume source": {
			contents:   "services:\n  web:\n    image: nginx\n    volumes:\n      - ${UPLOAD_LOCATION}/photos:/data\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.volumes",
		},
		"long volume source": {
			contents: "services:\n  web:\n    image: nginx\n    volumes:\n" +
				"      - type: bind\n        source: ${SOURCE}\n        target: /data\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.volumes",
		},
		"published host IP": {
			contents:   "services:\n  web:\n    image: nginx\n    ports:\n      - ${HOST_IP}:8080:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"composed published port": {
			contents: "services:\n  web:\n    image: nginx\n    ports:\n" +
				"      - target: 80\n        published: port-${PUBLISHED}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"unbraced published port": {
			contents:   "services:\n  web:\n    image: nginx\n    ports:\n      - $PUBLISHED:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"published port range": {
			contents:   "services:\n  web:\n    image: nginx\n    ports:\n      - ${START}-${END}:80\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"long target port": {
			contents: "services:\n  web:\n    image: nginx\n    ports:\n" +
				"      - target: ${TARGET}\n        published: 8080\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"long protocol": {
			contents: "services:\n  web:\n    image: nginx\n    ports:\n" +
				"      - target: 80\n        published: 8080\n        protocol: ${PROTOCOL}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.ports",
		},
		"network": {
			contents:   "services:\n  web:\n    image: nginx\n    networks:\n      - ${NETWORK}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.networks",
		},
		"network mode": {
			contents:   "services:\n  web:\n    image: nginx\n    network_mode: ${NETWORK_MODE}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.network_mode",
		},
		"privileged": {
			contents:   "services:\n  web:\n    image: nginx\n    privileged: ${PRIVILEGED}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.privileged",
		},
		"image": {
			contents:   "services:\n  web:\n    image: ${IMAGE}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.image",
		},
		"required image": {
			contents:   "services:\n  web:\n    image: ${IMAGE:?required}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.image",
		},
		"nested image default": {
			contents:   "services:\n  web:\n    image: ${IMAGE:-registry/${TENANT}/postgres:16}\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.image",
		},
		"nested volume default": {
			contents: "services:\n  web:\n    image: nginx\n    volumes:\n" +
				"      - ${SOURCE:-/srv/${TENANT}}:/data\n",
			diagnostic: "parse compose file: unresolved boundary interpolation in services.web.volumes",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", test.contents)
			_, err := composeparser.ParseFile(context.Background(), path)
			var incomplete *composeparser.IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want *compose.IncompleteError", err)
			}
			if incomplete.Error() != test.diagnostic {
				t.Fatalf("error = %q, want %q", incomplete.Error(), test.diagnostic)
			}
		})
	}
}

func TestParseFileWithOptionsResolvesBoundaryVariables(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  boundary:
    image: ${REGISTRY}/${IMAGE}
    ports:
      - ${HOST_IP}:${PUBLISHED}:${TARGET}/${PROTOCOL}
    networks:
      - ${NETWORK}
    volumes:
      - type: ${VOLUME_TYPE}
        source: ${SOURCE}/photos
        target: ${MOUNT_TARGET}
        read_only: ${READ_ONLY}
    privileged: ${PRIVILEGED}
  host:
    image: nginx
    network_mode: ${NETWORK_MODE}
networks:
  backend: {}
`)

	revision, err := composeparser.ParseFileWithOptions(
		context.Background(),
		path,
		composeparser.ParseOptions{Variables: map[string]string{
			"HOST_IP":      "127.0.0.1",
			"IMAGE":        "postgres:18",
			"MOUNT_TARGET": "/var/lib/postgresql/data",
			"NETWORK":      "backend",
			"NETWORK_MODE": "host",
			"PRIVILEGED":   "true",
			"PROTOCOL":     "udp",
			"PUBLISHED":    "8080",
			"READ_ONLY":    "true",
			"REGISTRY":     "example.invalid",
			"SOURCE":       "./data",
			"TARGET":       "80",
			"VOLUME_TYPE":  "bind",
		}},
	)
	if err != nil {
		t.Fatalf("ParseFileWithOptions() error = %v", err)
	}

	boundary := serviceNamed(t, revision, "boundary")
	if boundary.Image != "example.invalid/postgres:18" || !boundary.Privileged {
		t.Fatalf("boundary service = %#v", boundary)
	}
	if len(boundary.Bindings) != 1 || boundary.Bindings[0] != (graph.Binding{
		HostIP: "127.0.0.1", Published: "8080", Target: 80, Protocol: "udp",
	}) {
		t.Fatalf("bindings = %#v", boundary.Bindings)
	}
	if !slices.Equal(boundary.Networks, []string{"backend"}) {
		t.Fatalf("networks = %#v, want backend", boundary.Networks)
	}
	if len(boundary.Mounts) != 1 || boundary.Mounts[0].Type != "bind" ||
		boundary.Mounts[0].Source != "./data/photos" ||
		boundary.Mounts[0].Target != "/var/lib/postgresql/data" || !boundary.Mounts[0].ReadOnly {
		t.Fatalf("mounts = %#v", boundary.Mounts)
	}
	if host := serviceNamed(t, revision, "host"); !host.HostNetwork {
		t.Fatalf("host service = %#v, want host network", host)
	}
}

func TestParseFileWithOptionsResolvesOrPreservesSymbolicPublishedPort(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - ${HOST_IP}:${PUBLISHED}:80/${PROTOCOL}
`)

	partial := map[string]string{"HOST_IP": "127.0.0.1", "PROTOCOL": "udp"}
	symbolic, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: partial},
	)
	if err != nil {
		t.Fatalf("symbolic ParseFileWithOptions() error = %v", err)
	}
	resolved, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: map[string]string{
			"HOST_IP": "127.0.0.1", "PROTOCOL": "udp", "PUBLISHED": "8080",
		}},
	)
	if err != nil {
		t.Fatalf("resolved ParseFileWithOptions() error = %v", err)
	}
	symbolicBinding := serviceNamed(t, symbolic, "web").Bindings[0]
	if symbolicBinding.Published != "${PUBLISHED}" || symbolicBinding.HostIP != "127.0.0.1" || symbolicBinding.Protocol != "udp" {
		t.Fatalf("symbolic binding = %#v", symbolicBinding)
	}
	if got := serviceNamed(t, resolved, "web").Bindings[0].Published; got != "8080" {
		t.Fatalf("resolved published = %q", got)
	}
}

func TestParseFileWithOptionsKeepsExplicitLongPortDistinctFromSymbolicSentinel(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  explicit:
    image: nginx
    ports:
      - target: 80
        published: ${EXPLICIT}
  symbolic:
    image: nginx
    ports:
      - target: 81
        published: ${MISSING}
`)

	revision, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: map[string]string{"EXPLICIT": "65535"}},
	)
	if err != nil {
		t.Fatalf("ParseFileWithOptions() error = %v", err)
	}
	if got := serviceNamed(t, revision, "explicit").Bindings[0].Published; got != "65535" {
		t.Fatalf("explicit published = %q, want 65535", got)
	}
	if got := serviceNamed(t, revision, "symbolic").Bindings[0].Published; got != "${MISSING}" {
		t.Fatalf("symbolic published = %q, want ${MISSING}", got)
	}
}

func TestParseFileWithOptionsPreservesSymbolicPublishedPortWithTargetOperator(t *testing.T) {
	tests := map[string]struct {
		targetExpression string
		targetValue      string
		wantTarget       uint32
	}{
		"default":     {targetExpression: "${TARGET:-80}", targetValue: "81", wantTarget: 81},
		"alternative": {targetExpression: "${TARGET:+82}", targetValue: "set", wantTarget: 82},
		"required":    {targetExpression: "${TARGET:?required}", targetValue: "83", wantTarget: 83},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeCompose(t, t.TempDir(), "compose.yaml", fmt.Sprintf(`
services:
  web:
    image: nginx
    ports:
      - ${PUBLISHED}:%s
`, test.targetExpression))

			revision, err := composeparser.ParseFileWithOptions(
				context.Background(), path,
				composeparser.ParseOptions{Variables: map[string]string{"TARGET": test.targetValue}},
			)
			if err != nil {
				t.Fatalf("ParseFileWithOptions() error = %v", err)
			}
			binding := serviceNamed(t, revision, "web").Bindings[0]
			if binding.Published != "${PUBLISHED}" || binding.Target != test.wantTarget {
				t.Fatalf("binding = %#v, want symbolic published port and target %d", binding, test.wantTarget)
			}
		})
	}
}

func TestParseFileWithOptionsTreatsEmptyValueAsPresentAndIgnoresAmbientValue(t *testing.T) {
	t.Setenv("MODE", "host")
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    network_mode: ${MODE-default}
`)
	variables := map[string]string{"MODE": ""}

	revision, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: variables},
	)
	if err != nil {
		t.Fatalf("ParseFileWithOptions() error = %v", err)
	}
	variables["MODE"] = "host"
	if web := serviceNamed(t, revision, "web"); web.HostNetwork || !slices.Equal(web.Networks, []string{"default"}) {
		t.Fatalf("web = %#v, want explicit empty network mode", web)
	}
}

func TestParseFileWithOptionsKeepsMissingNonPublishedBoundaryIncomplete(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: ${IMAGE}
    volumes:
      - ${SOURCE}/photos:/data
`)

	_, err := composeparser.ParseFileWithOptions(
		context.Background(), path, composeparser.ParseOptions{Variables: map[string]string{"IMAGE": "nginx"}},
	)
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if got, want := incomplete.Error(), "parse compose file: unresolved boundary interpolation in services.web.volumes"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestParseFileUsesDeterministicBoundaryInterpolationDiagnostic(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  zed:
    image: nginx
    volumes:
      - ${ZED_PATH}/data:/data
  alpha:
    image: nginx
    networks:
      - ${ALPHA_NETWORK}
    ports:
      - ${ALPHA_PORT}:80
`)

	_, err := composeparser.ParseFile(context.Background(), path)
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if got, want := incomplete.Error(), "parse compose file: unresolved boundary interpolation in services.alpha.networks"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestParseFileResolvesInlineBoundaryDefaults(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: ${IMAGE:-postgres:16-alpine}
    ports:
      - ${PUBLISHED:-8080}:80
    volumes:
      - ${SOURCE:-./data}:/data
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	web := serviceNamed(t, revision, "web")
	if web.Image != "postgres:16-alpine" {
		t.Fatalf("image = %q, want inline image default", web.Image)
	}
	if len(web.Bindings) != 1 || web.Bindings[0].Published != "8080" {
		t.Fatalf("bindings = %#v, want inline published-port default", web.Bindings)
	}
	if len(web.Mounts) != 1 || web.Mounts[0].Source != "./data" {
		t.Fatalf("mounts = %#v, want inline source default", web.Mounts)
	}
}

func TestParseFileAllowsEscapedDollarInImage(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: $$DATABASE_IMAGE
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if got, want := serviceNamed(t, revision, "web").Image, "$DATABASE_IMAGE"; got != want {
		t.Fatalf("image = %q, want %q", got, want)
	}
}

func TestParseFileAllowsUnresolvedNonBoundaryInterpolation(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    command: ["sh", "-c", "echo ${MESSAGE}"]
    environment:
      VALUE: ${VALUE}
    healthcheck:
      test: ["CMD-SHELL", "echo ${CHECK}"]
    ports:
      - 127.0.0.1:8080:80
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
}

func TestParseFileAllowsUnresolvedIgnoredBoundaryMetadata(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  web:
    image: nginx
    ports:
      - name: web-${PORT_NAME}
        target: 80
        published: "8080"
        app_protocol: app-${APP_PROTOCOL}
    networks:
      default:
        aliases:
          - web-${NETWORK_ALIAS}
    volumes:
      - type: volume
        source: data
        target: /data
        volume:
          subpath: tenant-${VOLUME_SUBPATH}
volumes:
  data:
`)

	revision, err := composeparser.ParseFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if !revision.Complete {
		t.Fatal("revision.Complete = false, want true")
	}
}

func TestParseFileUsesStableLoaderDiagnostic(t *testing.T) {
	path := writeCompose(t, t.TempDir(), "compose.yaml", `
services:
  first:
    image: postgres
    volumes:
      - ${FIRST_PATH}:/data
  second:
    image: postgres
    volumes:
      - ${SECOND_PATH}:/var/lib/postgresql/data
`)

	_, err := composeparser.ParseFile(context.Background(), path)

	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if got, want := incomplete.Error(), "parse compose file: unresolved boundary interpolation in services.first.volumes"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func serviceNamed(t *testing.T, revision graph.Revision, name string) graph.Service {
	t.Helper()
	for _, service := range revision.Services {
		if service.Name == name {
			return service
		}
	}
	t.Fatalf("service %q not found in %#v", name, revision.Services)
	return graph.Service{}
}

func writeCompose(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func assertIncomplete(t *testing.T, err error) {
	t.Helper()
	var incomplete *composeparser.IncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("error = %v, want *compose.IncompleteError", err)
	}
	if incomplete.Error() == "" {
		t.Fatal("incomplete error is empty")
	}
}
