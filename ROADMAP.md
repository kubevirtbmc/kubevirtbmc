# Roadmap

This document describes where KubeVirtBMC is today, what we are working on now, and where we want to take it next.
It is a statement of direction, not a delivery contract. Priorities shift as adopters tell us what they need, so
treat dates as targets and expect items to move between releases.

The source of truth for individual work items is the
[issue tracker](https://github.com/kubevirtbmc/kubevirtbmc/issues) and its
[milestones](https://github.com/kubevirtbmc/kubevirtbmc/milestones). This file gives the bigger picture and links
out to the details.

## Where we are

KubeVirtBMC started as a SUSE Hack Week project to give KubeVirt VMs an IPMI interface so that
[Seeder](https://github.com/harvester/seeder) and [Tinkerbell](https://tinkerbell.org) could drive them like physical
servers. Since then it has grown into a small but complete virtual BMC, and [Metal3](https://metal3.io)/Ironic has
become the primary integration we test against:

- **API and controller.** A `v1beta1` `VirtualMachineBMC` CRD with webhook validation. Each BMC runs as its own
  `Deployment` with a dedicated Service, ServiceAccount, and RBAC. The Service can be exposed as `ClusterIP`,
  `NodePort`, or `LoadBalancer`.
- **IPMI.** Power control, chassis status, and boot device selection over RMCP+ / LANPLUS (built on `bougou/go-ipmi`).
  VM identity is exposed through FRU data. IPMI can be turned off per BMC.
- **Redfish.** Service root, sessions and Basic auth, `ComputerSystem` power actions, boot source override
  (`Once`/`Continuous`, `Legacy`/`UEFI`), and `Manager.Reset`.
- **Virtual media.** ISO insert and eject through CDI, including HTTPS sources, a configurable StorageClass and volume
  mode, and DataVolume size padding.
- **Real-BMC behaviour.** One-shot boot overrides that restore the original boot order, graceful vs. forced power
  actions mapped onto KubeVirt subresources, and retries during VM state transitions.
- **Operations.** Restricted-compliant security context, graceful shutdown, configurable agent resources, and
  structured access logs with request IDs.
- **Testing.** Unit and e2e tests on every change, the DMTF Redfish-Service-Validator and Interop-Validator in CI, and
  an end-to-end Metal3/Ironic provisioning suite.

The latest release is [v0.11.0](https://github.com/kubevirtbmc/kubevirtbmc/releases/tag/v0.11.0). Adopters include
SUSE (Harvester CI), CLASTIX, CESTC, and vCluster Labs. See [ADOPTERS.md](ADOPTERS.md).

## Now: v0.12.0

Target: end of October 2026. This is a short, focused release:

- Keep virtual media state across virtbmc pod restarts ([#325](https://github.com/kubevirtbmc/kubevirtbmc/issues/325)).
- Run virtbmc on its own, without the operator, for simple or air-gapped setups ([#270](https://github.com/kubevirtbmc/kubevirtbmc/issues/270)).
- Publish this roadmap ahead of the CNCF Sandbox application ([#329](https://github.com/kubevirtbmc/kubevirtbmc/issues/329)).

## Next: v0.13.0

Target: end of November 2026. The theme of this release is **making the BMC behave more like real hardware**, and
closing the gaps that bite in daily use.

Correctness and robustness

- Avoid DataVolume name and CD-ROM slot conflicts with media already on the VM ([#251](https://github.com/kubevirtbmc/kubevirtbmc/issues/251)).
- Enforce a one-to-one mapping between a `VirtualMachineBMC` and its `VirtualMachine` ([#277](https://github.com/kubevirtbmc/kubevirtbmc/issues/277)).

Features

- IPMI Serial over LAN, backed by the KubeVirt serial console ([#314](https://github.com/kubevirtbmc/kubevirtbmc/issues/314)).
- Finer-grained boot order control across IPMI and Redfish ([#222](https://github.com/kubevirtbmc/kubevirtbmc/issues/222)).
- A Redfish Task Service so clients can track `InsertMedia` / `EjectMedia` asynchronously ([#235](https://github.com/kubevirtbmc/kubevirtbmc/issues/235)).
- Populate Redfish resources with real VM data (CPU, memory, NICs, disks) instead of placeholders ([#45](https://github.com/kubevirtbmc/kubevirtbmc/issues/45)).
- Attach BMCs to networks other than the pod network, e.g. a Multus provisioning network ([#150](https://github.com/kubevirtbmc/kubevirtbmc/issues/150)).
- TLS for Redfish end to end ([#47](https://github.com/kubevirtbmc/kubevirtbmc/issues/47)).
- Make it straightforward for automation and AI agents to drive VMs through a standard BMC interface
  ([#309](https://github.com/kubevirtbmc/kubevirtbmc/issues/309)).

Project and contributor experience

- Versioned documentation and deploy previews for docs PRs ([#243](https://github.com/kubevirtbmc/kubevirtbmc/issues/243), [#258](https://github.com/kubevirtbmc/kubevirtbmc/issues/258)).
- A smoother `make run` loop against a local cluster ([#247](https://github.com/kubevirtbmc/kubevirtbmc/issues/247)).
- Controller cleanup: split out RBAC handling and refactor the Service controller ([#160](https://github.com/kubevirtbmc/kubevirtbmc/issues/160), [#161](https://github.com/kubevirtbmc/kubevirtbmc/issues/161)).

## Bare-metal compatibility track

This work starts in v0.13.0 alongside [#45](https://github.com/kubevirtbmc/kubevirtbmc/issues/45) and [#235](https://github.com/kubevirtbmc/kubevirtbmc/issues/235) and continues over the following releases.

Metal3, Tinkerbell, and Seeder need only a fairly small slice of Redfish. Newer bare-metal managers go further. They
check vendor strings, firmware versions, and serial numbers, rotate the factory password, read BIOS attributes,
and walk vendor OEM endpoints before they will accept a machine.

vCluster Labs has been running KubeVirtBMC against NVIDIA's bare-metal manager (NICo)
on their [`bare-metal-compat`](https://github.com/loft-sh/kubevirtbmc/tree/bare-metal-compat) branch, and it shows what
this takes in practice. With that branch, a KubeVirt VM can get through NICo's host initialisation and discovery as
either a host server or a BlueField-3 DPU. We want to bring that work upstream so that every user gets it, not just one
fork.

The pieces we plan to upstream, roughly in this order:

1. **Generic Redfish coverage that any client benefits from.** `Chassis`, `EthernetInterfaces`, `PCIeDevices`,
   `UpdateService` / `SimpleUpdate`, firmware inventory, `SecureBoot`, `BootOptions`, `Bios`, `AccountService`,
   `Manager/NetworkProtocol`, and `Manager.DateTime`. Empty collections should be `[]` and not `null`, and partial
   `PATCH` bodies should be accepted. Most of this overlaps with
   [#45](https://github.com/kubevirtbmc/kubevirtbmc/issues/45) and
   [#235](https://github.com/kubevirtbmc/kubevirtbmc/issues/235).
2. **Stable, configurable identity.** UUIDs and serials derived from the VM UID, plus vendor, model, firmware
   version, and serial overridable per VM. The fork does this with `bmc.kubevirtbmc.io/*` annotations. Upstream we
   expect to promote the stable parts into `VirtualMachineBMC` spec fields and keep annotations for experiments.
3. **Account management.** Password rotation through `AccountService`, with the new credential persisted as a hash
   rather than in clear text.
4. **BIOS attribute semantics.** Staged attributes that read back the way firmware does after a reboot. For example,
   `Tpm2Hierarchy=Clear` reads back as `Enabled`, which is what NICo polls for.
5. **Vendor profiles.** HPE iLO and Dell iDRAC OEM endpoints (BIOS and boot settings, job service, lockdown flags),
   and an NVIDIA BlueField DPU profile (OEM `nic_mode` / base MAC, rshim and mode actions, BFB install as a no-op that
   completes a task, CEC/NIC firmware inventory).

The key design question is how vendor-specific behaviour should live in the codebase. Our current thinking is a
**pluggable profile** selected per BMC (`generic`, `dell`, `hpe`, `nvidia-dpu`, …), so that the default stays a clean,
DMTF-conformant Redfish service that passes the validators. Vendor quirks are opt-in. We'll open a design discussion
before merging any of items 4 and 5. Feedback from anyone who has driven other managers against KubeVirtBMC is very
welcome there.

## Backlog

These issues are accepted but not yet tied to a milestone. Help is welcome on any of them.

- Stop advertising Redfish links we don't implement ([#275](https://github.com/kubevirtbmc/kubevirtbmc/issues/275)).
- Decouple BMC reconciliation from Service creation ([#135](https://github.com/kubevirtbmc/kubevirtbmc/issues/135)).
- Evaluate the Kubebuilder Helm plugin for chart generation ([#126](https://github.com/kubevirtbmc/kubevirtbmc/issues/126)).
- Helm-based e2e tests in CI ([#105](https://github.com/kubevirtbmc/kubevirtbmc/issues/105)).
- Load KubeVirt CRDs cleanly in tests ([#26](https://github.com/kubevirtbmc/kubevirtbmc/issues/26)).

## Later

These are directions we care about but haven't scheduled. If one of them matters to you, open an issue
or start a [discussion](https://github.com/kubevirtbmc/kubevirtbmc/discussions). That is the fastest way to move it up.

- **Scale and density.** Today it is one virtbmc Deployment per VM. For large CI fleets we want to explore a shared
  agent that serves many BMCs, and to cut per-BMC memory further.
- **Deeper Metal3 coverage.** Metal3/Ironic stays the reference integration. We want to extend the e2e suite beyond
  provisioning to cover inspection, virtual media boot, BIOS and firmware settings, and RAID/disk handling where it
  makes sense for a VM. That way every Ironic Redfish feature a user is likely to hit is exercised in CI.
- **More provisioning integrations.** Add e2e suites for Tinkerbell and Seeder next to the Metal3 one, and validate
  against the bare-metal managers covered by the compatibility track.
- **Redfish event service.** Push power and media state changes to subscribers instead of making clients poll.
- **Security hardening.** Credential rotation without pod restarts, finer-grained RBAC for the agent, and continued
  work towards the OpenSSF Best Practices passing level and beyond.
- **API graduation.** Once the spec has settled after the compatibility work, move `VirtualMachineBMC` towards `v1`
  with a documented upgrade path.
- **CNCF Sandbox.** Apply, then keep up the governance, security, and community practices that come with it.

## How this roadmap is maintained

Maintainers review this file at the start of each release cycle and update it when a milestone closes. Proposals for
new roadmap items should start as a GitHub issue or discussion. Larger ones, such as the vendor profile design above,
will get a short design doc in the repository before implementation starts.

If you're building something on top of KubeVirtBMC, we'd like to hear about it. Tell us in
[Discord](https://discord.gg/k5hT9GDQkY) or add yourself to [ADOPTERS.md](ADOPTERS.md). Real use cases are what
shape this roadmap.
