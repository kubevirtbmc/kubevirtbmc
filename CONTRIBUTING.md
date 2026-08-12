# Contributing to KubeVirtBMC

Thank you for your interest in contributing to KubeVirtBMC! This document provides guidelines and workflows to help you get started.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Community](#community)
- [Ways to Contribute](#ways-to-contribute)
- [Reporting Bugs](#reporting-bugs)
- [Suggesting Features](#suggesting-features)
- [Development Setup](#development-setup)
- [Development Workflow](#development-workflow)
- [Branching Strategy](#branching-strategy)
- [Commit Guidelines](#commit-guidelines)
- [Pull Request Process](#pull-request-process)
- [Code Review](#code-review)

## Code of Conduct

This project follows the [Contributor Covenant 3.0 Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold these standards. To report a violation, contact **conduct@kubevirtbmc.io**.

## Community

- **GitHub Issues** — Bug reports, feature requests, and enhancements: [github.com/kubevirtbmc/kubevirtbmc/issues](https://github.com/kubevirtbmc/kubevirtbmc/issues)
- **GitHub Discussions** — Questions and general conversation: [github.com/kubevirtbmc/kubevirtbmc/discussions](https://github.com/kubevirtbmc/kubevirtbmc/discussions)
- **Discord** — Real-time chat: [discord.gg/k5hT9GDQkY](https://discord.gg/k5hT9GDQkY)

## Ways to Contribute

- Fix bugs or implement features listed in GitHub Issues
- Improve or add documentation
- Write or improve tests
- Review pull requests
- Report bugs or suggest enhancements

## Reporting Bugs

Before filing a bug report, search existing issues to avoid duplicates. When opening a new issue, use the **Bug Report** template and include:

- KubeVirtBMC chart/app version
- Kubernetes distribution and version
- KubeVirt version
- Steps to reproduce
- Actual vs. expected behavior
- Relevant logs or screenshots

## Suggesting Features

Open an issue using the **Feature Request** or **Enhancement** template. Describe the use case and the problem it solves. For larger changes, consider discussing the design in a GitHub Discussion or on Discord before investing implementation effort.

## Development Setup

### Prerequisites

| Tool | Minimum Version | Notes |
|---|---|---|
| Go | 1.24 | [golang.org/dl](https://golang.org/dl/) |
| Docker | 20.10+ | Or compatible container engine |
| kubectl | 1.29+ | For cluster interaction |
| make | — | GNU Make |

All other build-time tools (kustomize, controller-gen, envtest, kind, golangci-lint, mockgen) are downloaded automatically by `make` targets into `./bin/`. You do not need to install them globally.

### Fork and Clone

```bash
# Fork via the GitHub UI, then:
git clone https://github.com/<your-username>/kubevirtbmc.git
cd kubevirtbmc
git remote add upstream https://github.com/kubevirtbmc/kubevirtbmc.git
```

### Verify the Setup

```bash
make build
```

## Development Workflow

Always work on a dedicated branch, never on `main`.

```bash
git checkout -b feat/my-feature   # or fix/issue-123
```

### Available Make Targets

```
make help          # Print all available targets
```

#### Code Generation

Regenerate manifests and Go code whenever you modify API types under `api/`:

```bash
make manifests generate generate-kubevirt-crd
```

The CI pipeline verifies that generated files are committed and up-to-date. Always run this before opening a PR if you changed any API types.

#### Formatting and Vetting

```bash
make fmt      # Run go fmt
make vet      # Run go vet
```

#### Linting

```bash
make lint          # Run golangci-lint
make lint-fix      # Run golangci-lint with auto-fix
```

The linter configuration lives in [`.golangci.yml`](.golangci.yml). Fix all reported issues before submitting a PR.

#### Building

```bash
make build                 # Build controller and virtbmc agent binaries
make docker-build          # Build Docker images for both components
```

#### Unit Tests

Unit tests use [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) and run without a live cluster:

```bash
make test
```

Coverage output is written to `cover.out`.

#### End-to-End Tests

E2E tests require a [kind](https://kind.sigs.k8s.io/) cluster. The full local workflow:

```bash
make local-e2e-test        # Create cluster, run e2e tests, delete cluster
```

To manage the lifecycle separately:

```bash
make e2e-setup             # Create the kind cluster (kvbmc-e2e)
make e2e-test              # Run e2e tests against the existing cluster
make e2e-teardown          # Delete the kind cluster
```

#### Running the Controller Locally

```bash
make run
```

This runs the controller binary directly against the cluster configured in `~/.kube/config`.

## Branching Strategy

KubeVirtBMC uses a **trunk-based model** for day-to-day development, combined with dedicated **release branches** for maintaining previously published minor versions. This is the same general pattern used by projects such as [Kubernetes](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-release/cherry-picks.md) and [Node.js](https://github.com/nodejs/node/blob/main/doc/contributing/backporting-to-release-lines.md), scaled down to fit this project.

### `main`

- `main` is the trunk. It always reflects the latest, unreleased state of the project.
- All new features and the vast majority of bug fixes are developed and merged here first.
- Your feature/fix branches (see [Development Workflow](#development-workflow)) are always cut from `main` and merged back into `main`.

### Release branches (`vX.Y`)

- Once a minor version is published (e.g. `v0.9.0`), a dedicated branch named after that minor version — `v0.9` — is cut from the release tag.
- The purpose of a release branch is to collect the commits that should ship in any future `vX.Y.Z` patch release (e.g. `v0.9.1`, `v0.9.2`, …), without pulling in unreleased features that have already landed on `main`.
- Only bug fixes are ever backported to a release branch — never new features or breaking changes.
- A maintainer tags a new patch release (`vX.Y.Z`) from the release branch once enough fixes have accumulated, or sooner if the fix is urgent (e.g. a security issue).

### Backporting a fix

A bug fix is expected to benefit every future release, so it always lands on `main` first and is then backported to whichever release branch(es) still need it:

1. Open your PR against `main` as usual, following the [Pull Request Process](#pull-request-process).
2. Once it's merged, decide whether the bug also affects a supported release branch (e.g. `v0.9`). If you're not sure, ask on Discord or in the PR thread — a maintainer will help.
3. Open a **separate** PR against that release branch that cherry-picks the merged commit(s):

   ```bash
   git fetch upstream
   git checkout -b backport/v0.9/my-fix upstream/v0.9
   git cherry-pick -x <commit-sha-from-main>
   git push -u origin backport/v0.9/my-fix
   ```

4. Title the backport PR `[v0.9 backport] <original PR title>` and reference the original PR in the description (e.g. `Backport of #123`).
5. Backport PRs go through the same review, CI, and DCO requirements as any other PR — they are not fast-tracked.

## Commit Guidelines

This project follows [Conventional Commits](https://www.conventionalcommits.org/). Every commit message must conform to the format:

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

### Types

| Type | When to use |
|---|---|
| `feat` | New feature |
| `fix` | Bug fix |
| `docs` | Documentation changes only |
| `test` | Adding or correcting tests |
| `refactor` | Code change that is neither a fix nor a feature |
| `chore` | Build system, tooling, dependency updates |
| `ci` | CI configuration changes |
| `perf` | Performance improvements |

### Examples

```
feat(redfish): add virtual media eject support
fix(ipmi): handle power-off timeout for suspended VMs
docs: add e2e test setup instructions to CONTRIBUTING.md
test(controller): add unit tests for boot order reconciliation
```

For breaking changes, append `!` after the type or add a `BREAKING CHANGE:` footer:

```
feat!: rename VirtualMachineBMC spec field ipmiEnabled to ipmi.enabled

BREAKING CHANGE: The `spec.ipmiEnabled` field has been renamed to `spec.ipmi.enabled`.
```

### Developer Certificate of Origin (DCO)

All commits must be signed off to certify you have the right to submit the contribution under the project license ([Apache 2.0](LICENSE)). Use the `-s` / `--signoff` flag:

```bash
git commit -s -m "feat: my contribution"
```

This appends `Signed-off-by: Your Name <your@email.com>` to the commit message. Commits without a sign-off will be blocked by CI.

## Pull Request Process

1. **Ensure your branch is up-to-date** with upstream `main` before opening a PR:

   ```bash
   git fetch upstream
   git rebase upstream/main
   ```

2. **Run the full local validation suite** before pushing:

   ```bash
   make manifests generate generate-kubevirt-crd
   make fmt vet lint test
   ```

3. **Open the PR** against the `main` branch of `kubevirtbmc/kubevirtbmc`. Bug fixes always target `main` first, even if they also apply to an older release line — see [Branching Strategy](#branching-strategy) for how backports work.

4. **Fill in the PR description** with:
   - What problem does this solve?
   - What was changed and why?
   - How was it tested?
   - Reference to the related issue (e.g., `Closes #123`)

5. **CI must pass** — the pipeline runs lint, unit tests, binary build, and e2e tests. Investigate and fix any failures.

6. **Address review feedback** by pushing additional commits. Do not force-push to a PR branch that is under review unless a maintainer requests it.

7. **A maintainer will merge** the PR once it has at least one approval and all checks are green.

### PR Checklist

- [ ] Code compiles (`make build`)
- [ ] Unit tests pass (`make test`)
- [ ] No new lint violations (`make lint`)
- [ ] Generated files are up-to-date (`make manifests generate generate-kubevirt-crd`)
- [ ] All commits are signed off (`git commit -s`)
- [ ] PR description references the related issue

## Code Review

- Reviewers aim to respond within a few business days.
- Be respectful and constructive. Refer to our [Code of Conduct](CODE_OF_CONDUCT.md).
- Prefix non-blocking comments with **nit:** to distinguish them from required changes.
- If a review request is urgent, ping the thread or reach out on Discord.

Maintainers may close PRs that have had no activity for 30 days with the `stale` label applied first as a warning.

Thank you for contributing to KubeVirtBMC!
