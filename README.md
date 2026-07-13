# ExposureMap

ExposureMap compares two revisions of container configuration and explains changes to reachability and compromise blast radius.

The project is an early prototype. It has a warning-only, single-file Docker Compose analyzer plus the public-corpus and blinded-review workflow used to measure precision before any hosted service is built.

## The review question

ExposureMap is designed to answer:

> Does this change expose anything new or weaken an existing boundary?

It models observed bindings as:

- `host-exposed`: published on a host interface.
- `proxy-routed`: reachable through a recognized proxy route.
- `internal`: reachable only through observed container networks.
- `unknown`: static configuration is insufficient for a verdict.

ExposureMap will not call a service Internet-accessible unless explicit project context identifies a public interface, host, or network.

## Current analyzer slice

The current CLI accepts one regular Compose file for each revision. It normalizes services, published ports, Compose networks, bind mounts, privileged mode, and host network mode with the reference `compose-go` loader.

It reports four deterministic change families:

- `EM-COMPOSE-001`: new host port.
- `EM-COMPOSE-002`: loopback binding widened to a wildcard address.
- `EM-COMPOSE-003`: host network mode introduced.
- `EM-COMPOSE-004`: a host-exposed service becomes privileged or gains a sensitive bind mount.

Run a comparison as JSON:

```sh
go run ./cmd/exposuremap diff \
  --base path/to/base/compose.yaml \
  --head path/to/head/compose.yaml \
  --format json
```

Use `--format markdown` for a review-oriented report. Complete analyses are deliberately reported as `warn`, even with no findings. Unsupported or unresolved inputs are `incomplete`. Both exit `0`; this prototype cannot become an accidental CI gate. Invalid invocations and output failures exit `2`.

Compose overrides, includes, extends, profiles, environment/label files, Dockerfiles, proxy configuration, and policy are not analyzed yet. The adapter rejects external Compose inputs instead of reading them implicitly. Static nginx is planned after the first release. Kubernetes, Terraform, live Docker state, firewalls, DNS, Nginx Proxy Manager, and Apache are outside the first release.

## What exists now

- A versioned, metadata-only benchmark corpus format.
- Twelve researched public-repository seeds with license metadata.
- Path-first GitHub history mining that avoids enumerating every pull request.
- Repository-level training, validation, and evaluation splits.
- Independent primary/secondary review with required human adjudication.
- Exact technical gates for completeness, resolution, precision, usefulness, and false CI failures.
- Immutable case materialization with upstream blob SHAs, local SHA-256 checksums, and bounded private cache files.
- A deterministic graph, four Compose boundary rules, and stable JSON/Markdown reports.
- Atomic benchmark run bundles with scorer-compatible results and per-case review reports.
- Deterministic training-derived mutations for each implemented rule.

Run the current tests:

```sh
go test -race ./...
```

Inspect the CLI:

```sh
go run ./cmd/exposuremap help
```

See [benchmark/README.md](benchmark/README.md) for the corpus and reviewer workflow.

## Safety boundary

The analyzer will never execute repository code, builds, hooks, containers, Docker, or proxy binaries. It will be deterministic and will not use an LLM for analysis or verdicts.

A report with no findings means only "no boundary regression detected within observed inputs." It is not proof that a deployment is secure. Runtime state, firewall rules, DNS, application authentication, vulnerabilities, and generated configuration may remain unobserved.

## License

Apache-2.0. The CLI, analyzer, rules, GitHub integration, and API are intended to remain self-hostable.
