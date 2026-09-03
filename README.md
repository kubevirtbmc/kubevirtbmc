# KubeVirtBMC

<div align="center">
  <img src="docs/assets/images/kubevirtbmc_banner_tagline_transparent.png" alt="KubeVirtBMC"/>
</div>

[![main build and publish workflow](https://github.com/kubevirtbmc/kubevirtbmc/actions/workflows/main.yml/badge.svg)](https://github.com/kubevirtbmc/kubevirtbmc/actions/workflows/main.yml)
[![codecov](https://codecov.io/gh/kubevirtbmc/kubevirtbmc/graph/badge.svg?token=6U2WviF0iB)](https://codecov.io/gh/kubevirtbmc/kubevirtbmc)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/14432/badge)](https://www.bestpractices.dev/projects/14432)
[![License](https://img.shields.io/github/license/kubevirtbmc/kubevirtbmc)](https://github.com/kubevirtbmc/kubevirtbmc/blob/main/LICENSE)
[![release](https://img.shields.io/github/v/release/kubevirtbmc/kubevirtbmc)](https://github.com/kubevirtbmc/kubevirtbmc/releases)
[![Discord](https://img.shields.io/badge/Discord-join_chat-blue?style=social&logo=discord)](https://discord.gg/k5hT9GDQkY)

KubeVirtBMC provides **out-of-band management** for [KubeVirt](https://github.com/kubevirt/kubevirt) virtual machines on Kubernetes via [IPMI](https://www.intel.com.tw/content/www/tw/zh/products/docs/servers/ipmi/ipmi-second-gen-interface-spec-v2-rev1-1.html) and [Redfish](https://www.dmtf.org/standards/redfish)—power on/off, reset, and set boot device. Built for [Tinkerbell](https://github.com/tinkerbell/tink)/[Seeder](https://github.com/harvester/seeder) and compatible with any tooling that speaks IPMI or Redfish.

The project began in [SUSE Hack Week 23](https://hackweek.opensuse.org/), added Redfish in [Hack Week 24](https://hackweek.opensuse.org/24/projects/extending-kubevirtbmcs-capability-by-adding-redfish-support), and Redfish virtual media after [Hack Week 25](https://hackweek.opensuse.org/25/projects/preparing-kubevirtbmc-for-project-transfer-to-the-kubevirt-organization).


## Documentation

For installation, quick start, and usage, see the [documentation site](https://docs.kubevirtbmc.io). The project is developed on [GitHub](https://github.com/kubevirtbmc/kubevirtbmc).

## Contributing

We welcome contributions. Please open issues and pull requests in the [repository](https://github.com/kubevirtbmc/kubevirtbmc).


## License

KubeVirtBMC is licensed under the Apache License, Version 2.0.
See [LICENSE](./LICENSE).
