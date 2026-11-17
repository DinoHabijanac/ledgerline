# ADR 001: kind + ko as the local platform

Status: accepted 29 Oct 2026, recorded retroactively 17 Nov 2026.

## Context
<Why you needed a local Kubernetes at all instead of docker compose or plain `go run`.
Mention the hard constraints that drove it: 8 GB laptop, WSL2 capped at 5 GB, EUR 0 budget,
and the fact that the project has to demonstrate SRE work on Kubernetes to be credible as
proof-of-work for an SRE/platform role.>

## Decision
<kind for the cluster, ko for building and deploying Go services.
Say what ko actually does for you: no Dockerfile, no docker build, no registry --
`KO_DOCKER_REPO=kind.local` builds the Go binary and loads the image straight into
the kind node. Say what the image ends up being (distroless, non-root) and why that
matters later for the CRA/supply-chain phase.>

## Alternatives
<docker compose: lighter, but then none of the Kubernetes work (probes, rolling updates,
chaos, operators like CNPG) is demonstrable -- wrong tool for the goal.
k3d / minikube: comparable, say in one line why kind won.
Dockerfile + docker build + kind load: what ko saves you per iteration.
A managed cloud cluster: breaks the EUR 0 constraint.>

## Consequences
<+ fast iteration (roughly how many seconds from code change to running pod?);
+ small, non-root images for free, useful evidence for CRA later;
+ real Kubernetes semantics to break in the chaos phase.
- cluster is recreated from zero every session, so bring-up must be scripted;
- single node means node-failure chaos has to happen in Codespaces;
- ko only builds Go, so anything non-Go needs a different path.>

Replace each <...> with one or two sentences of your own.
