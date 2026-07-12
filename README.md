# ExposureMap

ExposureMap compares two revisions of container configuration and explains changes to reachability and compromise blast radius.

The project is an early prototype. The repository currently contains the public-corpus and blinded-review foundation used to prove that the analyzer can be precise and useful before the analyzer or hosted service is built.

## The review question

ExposureMap is designed to answer:

> Does this change expose anything new or weaken an existing boundary?

It will model bindings as:

- `host-exposed`: published on a host interface.
- `proxy-routed`: reachable through a recognized proxy route.
- `internal`: reachable only through observed container networks.
- `unknown`: static configuration is insufficient for a verdict.

ExposureMap will not call a service Internet-accessible unless explicit project context identifies a public interface, host, or network.

## Planned first inputs

- Docker Compose, including overrides, includes, and extends.
- Dockerfiles referenced by Compose builds.
- Traefik Docker labels.
- Caddyfiles referenced from the repository.
- Optional `.exposuremap.yml` context and policy.

Static nginx support is planned after the first release. Kubernetes, Terraform, live Docker state, firewalls, DNS, Nginx Proxy Manager, and Apache are outside the first release.

## What exists now

- A versioned, metadata-only benchmark corpus format.
- Twelve researched public-repository seeds with license metadata.
- Path-first GitHub history mining that avoids enumerating every pull request.
- Repository-level training, validation, and evaluation splits.
- Independent primary/secondary review with required human adjudication.
- Exact technical gates for completeness, resolution, precision, usefulness, and false CI failures.

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

A clean report will mean only "no boundary regression detected within observed inputs." It is not proof that a deployment is secure. Runtime state, firewall rules, DNS, application authentication, vulnerabilities, and generated configuration may remain unobserved.

## License

Apache-2.0. The CLI, analyzer, rules, GitHub integration, and API are intended to remain self-hostable.
