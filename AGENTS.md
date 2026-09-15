# KubeVirtBMC

Guidance for AI coding agents. Human contributor docs: [CONTRIBUTING.md](CONTRIBUTING.md).

## The three repositories

| Repository | Owns |
|---|---|
| `kubevirtbmc/kubevirtbmc` (this repo) | Controller and virtbmc agent, API types (`api/`), generated manifests (`config/`) |
| `kubevirtbmc/chart` | Helm chart that deploys KubeVirtBMC |
| `kubevirtbmc/docs` | User-facing documentation site |

## Cross-repo obligations

- **Chart sync.** `kubevirtbmc-bot` mirrors `config/crd/bases/**` into `kubevirtbmc/chart` automatically. Everything else your change touches under `config/` — RBAC, webhook, manager manifests — is **not** synced: open a companion PR against `kubevirtbmc/chart` alongside your code PR, not after release.
- **Docs.** User-facing documentation lives in `kubevirtbmc/docs` — changes that alter user-visible behavior need a PR there.

## Protocol specifications

KubeVirtBMC speaks two protocols governed by official specifications. Parsing, message construction, and interaction validation must align with them — do not invent non-standard behavior. If a scenario forces an adaptation, build it on the core specification and record the rationale.

- **Redfish** — DMTF Redfish Specification ([DSP0266](https://www.dmtf.org/dsp/DSP0266)), live schemas at [redfish.dmtf.org](https://redfish.dmtf.org). Checked in: OpenAPI `hack/redfish/spec/openapi.yaml`; the schema bundle is gitignored — `make download-schema` fetches it into `hack/DSP8010_*/`.
- **IPMI** — [Intelligent Platform Management Interface Specification, Second Generation v2.0 rev 1.1](https://www.intel.com/content/dam/www/public/us/en/documents/product-briefs/ipmi-second-gen-interface-spec-v2-rev1-1.pdf) (PDF)

**Reference consumers: Metal3/Ironic (sushy for Redfish, ipmitool for IPMI).** Spec compliance is the floor, not the bar — fields those clients validate must actually be populated (e.g. `Boot@Redfish.AllowableValues`, `Status.Health`), and commands they issue unconditionally must not error.

## Commands

`make help` prints every target. The ones every code change needs:

```bash
make build test lint
make manifests generate generate-kubevirt-crd  # required when api/ or Redfish routes change
make redfish-interop  # required when Redfish responses or routes change; CI enforces it
```

## Generated files are read-only

`zz_generated.deepcopy.go`, `pkg/redfish/implemented_routes_gen.go`, `config/crd/bases/`, `config/kubevirt-crd/`, and controller-gen RBAC/webhook output under `config/`. Regenerate with the targets above; never hand-edit — CI fails on a dirty tree.

## Style

- Keep changes scoped to the ask — no drive-by refactors or unrequested hardening.
- Comments are the exception, and review will ask you to trim them. Write one only when it explains non-obvious rationale or complex business logic (concisely), feeds a tool (`//nolint:<rule> // reason`, `// +kubebuilder:` markers), or tracks follow-up work (TODO/FIXME with an issue link). If the code already says it, the comment goes.
- Tests prove behavior: a bug fix adds one regression test that fails without the fix. Test function, not structure.
- Error messages name what failed and why; wrap the cause with `%w`.

## Commits and PRs

- [Conventional Commits](https://www.conventionalcommits.org/), DCO sign-off (`git commit -s`).
- Fixes land on `main` first, then backport to release branches.
- Every change tracks an issue — if none exists for the problem, open one first; no PR-only changes.
- Reference the issue with a plain `#123`, never auto-close keywords (`fixes`/`closes`/`resolves`): an issue can span several PRs (code, chart, docs, backports), and a maintainer closes it once all of them land.

Full rules: [CONTRIBUTING.md](CONTRIBUTING.md).
