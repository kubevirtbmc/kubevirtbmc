# VERSION and COMMIT are set by the CI/CD pipeline. If not set, they are set to
# the current branch and commit.
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "$(shell git rev-parse --abbrev-ref HEAD)-head")
COMMIT ?= $(shell git rev-parse HEAD)

DIRTY :=
ifneq ($(shell git status --porcelain --untracked-files=no),)
DIRTY := -dirty
endif
VERSION := $(VERSION)$(DIRTY)
# Sanitize for Docker image tag: replace chars not in [a-zA-Z0-9_.-] with '-'
export TAG = $(shell echo "$(VERSION)" | sed 's|[^a-zA-Z0-9_.-]|-|g')

export REPO ?= kubevirtbmc

# Image URL to use all building/pushing image targets
MGR_IMG ?= $(REPO)/virtbmc-controller:$(TAG)
AGT_IMG ?= $(REPO)/virtbmc:$(TAG)

K8S_VERSION = 1.36.4
# TODO: The inconsistency between k8s version and kind node image version is a temporary hack.
KIND_K8S_VERSION = v1.36.1
# KIND_K8S_VERSION = v$(shell echo $(K8S_VERSION))
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.36.x
export CERT_MANAGER_VERSION = v1.21.1

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# KUBEVIRT API version to use (CRD codegen); keep in sync with KUBEVIRT_VERSION.
KUBEVIRT_API_VERSION = v1.9.0
# Runtime install pin for e2e (test/util InstallKubeVirt).
export KUBEVIRT_VERSION ?= $(KUBEVIRT_API_VERSION)
export CDI_VERSION ?= v1.66.0

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen generate-implemented-routes ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: generate-implemented-routes
generate-implemented-routes: ## Derive the implemented Redfish route set from pkg/redfish/api_service.go.
	go generate ./pkg/redfish

.PHONY: generate-kubevirt-crd
generate-kubevirt-crd: controller-gen ## Clone KubeVirt API and generate CustomResourceDefinition objects for integration testing purposes.
	set -euo pipefail; \
	TMP_DIR=$$(mktemp -d -p /tmp/); \
	trap 'rm -rf "$$TMP_DIR"' EXIT; \
	KUBEVIRT_API_DIR=$$TMP_DIR/kubevirt-api; \
	git clone --depth 1 --branch $(KUBEVIRT_API_VERSION) https://github.com/kubevirt/api $$KUBEVIRT_API_DIR; \
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths="$$KUBEVIRT_API_DIR/core/v1/..." output:crd:artifacts:config=config/kubevirt-crd; \
	rm -vf config/kubevirt-crd/kubevirt.io_datavolumetemplatespecs.yaml

.PHONY: generate-mock
generate-mock: mockgen ## Generate mocks for interfaces.
	$(MOCKGEN) -source=pkg/resourcemanager/resourcemanager.go -destination=pkg/resourcemanager/mock_resourcemanager.go -package=resourcemanager

REDFISH_SCHEMA_BUNDLE ?= DSP8010_2024.3
.PHONY: download-redfish-schema
download-schema: ## Download the Redfish schema.
	test -d ./hack/$(REDFISH_SCHEMA_BUNDLE) || \
	( curl -sSL https://www.dmtf.org/sites/default/files/standards/documents/$(REDFISH_SCHEMA_BUNDLE).zip -o ./hack/$(REDFISH_SCHEMA_BUNDLE).zip && \
	unzip -q -d ./hack/ ./hack/$(REDFISH_SCHEMA_BUNDLE).zip && \
	rm -f ./hack/$(REDFISH_SCHEMA_BUNDLE).zip )

.PHONY: generate-redfish-api
generate-redfish-api: ## Generate Redfish API server.
	./hack/redfish/generate.sh

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate generate-kubevirt-crd fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $(shell go list ./... | grep -v /test/) -coverprofile cover.out

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.13.1
golangci-lint:
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) $(GOLANGCI_LINT_VERSION) ;\
	}

.PHONY: e2e-setup
e2e-setup: kind cloud-provider-kind ## Setup end-to-end test environment.
	@$(KIND) get clusters 2>/dev/null | grep -q kvbmc-e2e || \
		$(KIND) create cluster --name kvbmc-e2e --config test/kind-config.yaml --image=kindest/node:$(KIND_K8S_VERSION)
	@# Control-plane nodes are excluded from external LoadBalancer targets by default;
	@# remove the label so cloud-provider-kind can route to workloads scheduled there too.
	@$(KUBECTL) label node --all node.kubernetes.io/exclude-from-external-load-balancers- --overwrite >/dev/null 2>&1 || true
	@# cloud-provider-kind (https://kind.sigs.k8s.io/docs/user/loadbalancer/) must run continuously
	@# in the background to watch kind clusters and assign LoadBalancer Service ingress IPs.
	@if [ ! -f $(CLOUD_PROVIDER_KIND_PID_FILE) ] || ! kill -0 $$(cat $(CLOUD_PROVIDER_KIND_PID_FILE)) 2>/dev/null; then \
		echo "Starting cloud-provider-kind..."; \
		nohup $(CLOUD_PROVIDER_KIND) > $(CLOUD_PROVIDER_KIND_LOG_FILE) 2>&1 & \
		echo $$! > $(CLOUD_PROVIDER_KIND_PID_FILE); \
		sleep 2; \
	else \
		echo "cloud-provider-kind already running (pid $$(cat $(CLOUD_PROVIDER_KIND_PID_FILE)))"; \
	fi

.PHONY: e2e-teardown
e2e-teardown: kind ## Teardown end-to-end test environment.
ifeq ($(KEEP_ENV),true)
	@echo "KEEP_ENV=true, skipping e2e-teardown"
else
	@if [ -f $(CLOUD_PROVIDER_KIND_PID_FILE) ]; then \
		echo "Stopping cloud-provider-kind..."; \
		kill $$(cat $(CLOUD_PROVIDER_KIND_PID_FILE)) 2>/dev/null || true; \
		rm -f $(CLOUD_PROVIDER_KIND_PID_FILE); \
	fi
	$(KIND) delete cluster --name kvbmc-e2e
endif

.PHONY: e2e-test
e2e-test: generate fmt vet kind ## Run end-to-end tests (controller first, then agent: IPMI, Redfish, Virtual Media).
	go test -v -timeout 15m ./test/virtbmc-controller/...
	go test -v -timeout 15m ./test/virtbmc-agent/...

.PHONY: local-e2e-test
local-e2e-test: e2e-setup e2e-test e2e-teardown ## Run end-to-end tests locally.

##@ Metal3 / Ironic integration e2e (release-grade; not run on PRs)

# External dependency versions — exported into hack/metal3-e2e/*.sh and go e2e.
# Override per-run: make metal3-e2e-setup IRSO_VERSION=v0.11.0
export IRSO_VERSION ?= v0.10.0
export BMO_VERSION ?= v0.13.2
export IRONIC_VERSION ?= 37.0
export MULTUS_VERSION ?= v4.2.2
export CNI_PLUGINS_VERSION ?= v1.6.2
# CERT_MANAGER_VERSION / KUBEVIRT_VERSION / CDI_VERSION are exported above.

METAL3_CLUSTER ?= kvbmc-metal3-e2e

.PHONY: metal3-e2e-setup
metal3-e2e-setup: kind ## Create single-node Kind + br-prov + Multus + IrSO/BMO.
	@$(KIND) get clusters 2>/dev/null | grep -q $(METAL3_CLUSTER) || \
		$(KIND) create cluster --name $(METAL3_CLUSTER) --config test/kind-config-metal3.yaml --image=kindest/node:$(KIND_K8S_VERSION)
	@CLUSTER_NAME=$(METAL3_CLUSTER) bash hack/metal3-e2e/setup-prov-net.sh
	@CLUSTER_NAME=$(METAL3_CLUSTER) bash hack/metal3-e2e/install-metal3.sh

.PHONY: metal3-e2e-teardown
metal3-e2e-teardown: kind ## Tear down Metal3 stack, Multus NAD/br-prov, and Kind cluster. KEEP_ENV=true skips.
ifeq ($(KEEP_ENV),true)
	@echo "KEEP_ENV=true, skipping metal3-e2e-teardown"
else
	$(KIND) delete cluster --name $(METAL3_CLUSTER)
endif

.PHONY: metal3-e2e-test
metal3-e2e-test: generate fmt vet ## Run Metal3/Ironic integration tests (requires metal3-e2e-setup + built images).
	# go test -timeout bounds the process; -ginkgo.timeout bounds the suite (Ginkgo default is 1h).
	KIND_CLUSTER=$(METAL3_CLUSTER) go test -v ./test/metal3-e2e/... -ginkgo.v -ginkgo.timeout=120m -timeout 120m

.PHONY: metal3-e2e-diagnostics
metal3-e2e-diagnostics: ## Dump Metal3 e2e diagnostics (pods, BMH, Ironic logs) into ./artifacts. Best-effort, never fails.
	@CLUSTER_NAME=$(METAL3_CLUSTER) bash hack/metal3-e2e/collect-diagnostics.sh

.PHONY: local-metal3-e2e-test
local-metal3-e2e-test: metal3-e2e-setup metal3-e2e-test metal3-e2e-teardown ## Full Metal3 e2e locally (heavy). KEEP_ENV=true keeps Kind+stack.

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

##@ Build

LINKFLAGS := "-s -w -X main.AppVersion=$(VERSION) -X main.GitCommit=$(COMMIT)"

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -trimpath -ldflags $(LINKFLAGS) -o bin/manager cmd/controller/main.go
	go build -trimpath -ldflags $(LINKFLAGS) -o bin/virtbmc cmd/virtbmc/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/controller/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: generate-implemented-routes ## Build docker images with the manager and the agent respectively.
	$(CONTAINER_TOOL) build -t $(MGR_IMG) --build-arg LINKFLAGS=$(LINKFLAGS) .
	$(CONTAINER_TOOL) build -t $(AGT_IMG) --build-arg LINKFLAGS=$(LINKFLAGS) --build-arg TARGETARCH=amd64 -f Dockerfile.virtbmc .
ifeq ($(PUSH),true)
	$(CONTAINER_TOOL) push $(MGR_IMG)
	$(CONTAINER_TOOL) push $(AGT_IMG)
endif

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: generate-implemented-routes ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile.virtbmc > Dockerfile.virtbmc.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --build-arg LINKFLAGS=$(LINKFLAGS) --tag $(MGR_IMG) -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --build-arg LINKFLAGS=$(LINKFLAGS) --tag $(AGT_IMG) -f Dockerfile.virtbmc.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.cross
	rm Dockerfile.virtbmc.cross

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

IMG ?= $(MGR_IMG)
.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

INSTALL_MANIFEST ?= kubevirtbmc-install.yaml
.PHONY: generate-install-manifest
generate-install-manifest: manifests kustomize
	@echo "Generating install manifest with image: $(IMG)"
	@mkdir -p $(dir $(INSTALL_MANIFEST))
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > $(INSTALL_MANIFEST)
	@echo "Generated $(INSTALL_MANIFEST)"

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
KIND ?= $(LOCALBIN)/kind
MOCKGEN ?= $(LOCALBIN)/mockgen
CLOUD_PROVIDER_KIND ?= $(LOCALBIN)/cloud-provider-kind
CLOUD_PROVIDER_KIND_PID_FILE ?= $(LOCALBIN)/cloud-provider-kind.pid
CLOUD_PROVIDER_KIND_LOG_FILE ?= $(LOCALBIN)/cloud-provider-kind.log

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.21.0
KIND_VERSION ?= v0.32.0
MOCKGEN_VERSION ?= v0.6.0
CLOUD_PROVIDER_KIND_VERSION ?= v0.11.1

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: kind
kind: $(LOCALBIN) ## Download kind locally if necessary. If wrong version is installed, it will be removed before downloading.
	@if test -x "$(KIND)" && ! "$(KIND)" version | grep -q "$(KIND_VERSION)"; then \
		echo "$(KIND) version is not expected $(KIND_VERSION). Removing it before installing."; \
		rm -f "$(KIND)"; \
	fi
	test -s "$(KIND)" || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/kind@$(KIND_VERSION)

.PHONY: mockgen
mockgen: $(MOCKGEN) ## Download mockgen locally if necessary.
$(MOCKGEN): $(LOCALBIN)
	test -s $(LOCALBIN)/mockgen || GOBIN=$(LOCALBIN) go install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)

.PHONY: cloud-provider-kind
cloud-provider-kind: $(CLOUD_PROVIDER_KIND) ## Download cloud-provider-kind locally if necessary. Provides LoadBalancer Service support for kind clusters.
$(CLOUD_PROVIDER_KIND): $(LOCALBIN)
	test -s $(LOCALBIN)/cloud-provider-kind || GOBIN=$(LOCALBIN) GO111MODULE=on go install sigs.k8s.io/cloud-provider-kind@$(CLOUD_PROVIDER_KIND_VERSION)
