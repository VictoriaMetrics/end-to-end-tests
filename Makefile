# Makefile for VictoriaMetrics End-to-End Tests

# Dependencies versions
GO_VERSION ?= 1.26.1
KIND_VERSION ?= v0.31.0
KUBECTL_VERSION ?= v1.35.0
CRUST_GATHER_VERSION ?= v0.12.1
VMGATHER_VERSION ?= v1.9.1
GINKGO_VERSION ?= latest
TERRAFORM_VERSION ?= 1.9.0

# Image versions
OPERATOR_REGISTRY ?= quay.io
OPERATOR_REPOSITORY ?= victoriametrics/operator
OPERATOR_TAG ?= v0.67.0

VM_VMSINGLEDEFAULT_VERSION ?= v1.122.14-enterprise
VM_VMCLUSTERDEFAULT_VERSION ?= v1.122.14-cluster-enterprise

VM_VMSINGLEDEFAULT_IMAGE ?= quay.io/victoriametrics/victoria-metrics

VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE ?= quay.io/victoriametrics/vmselect
VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION ?= $(VM_VMCLUSTERDEFAULT_VERSION)

VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE ?= quay.io/victoriametrics/vmstorage
VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION ?= $(VM_VMCLUSTERDEFAULT_VERSION)

VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE ?= quay.io/victoriametrics/vminsert
VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION ?= $(VM_VMCLUSTERDEFAULT_VERSION)

VM_VMAGENTDEFAULT_IMAGE ?= quay.io/victoriametrics/vmagent
VM_VMAGENTDEFAULT_VERSION ?= $(VM_VMSINGLEDEFAULT_VERSION)

VM_VMALERTDEFAULT_IMAGE ?= quay.io/victoriametrics/vmalert
VM_VMALERTDEFAULT_VERSION ?= $(VM_VMSINGLEDEFAULT_VERSION)

VM_VMAUTHDEFAULT_IMAGE ?= quay.io/victoriametrics/vmauth
VM_VMAUTHDEFAULT_VERSION ?= $(VM_VMSINGLEDEFAULT_VERSION)

VM_VMBACKUPDEFAULT_IMAGE ?= quay.io/victoriametrics/vmbackup
VM_VMBACKUPDEFAULT_VERSION ?= $(VM_VMSINGLEDEFAULT_VERSION)

VM_VMRESTOREDEFAULT_IMAGE ?= quay.io/victoriametrics/vmrestore
VM_VMRESTOREDEFAULT_VERSION ?= $(VM_VMSINGLEDEFAULT_VERSION)

LICENSE_FILE ?=

VM_ENTERPRISE ?=

# Configuration
BIN_DIR := $(shell pwd)/bin
GOPATH_BIN := $(shell go env GOPATH)/bin
export PATH := $(BIN_DIR):$(GOPATH_BIN):$(PATH)
GCP_REGION ?= europe-central2
DISTRIBUTED_ZONES ?= $(GCP_REGION)-a,$(GCP_REGION)-b,$(GCP_REGION)-c

OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)

ifeq ($(ARCH),x86_64)
ARCH := amd64
endif
ifeq ($(ARCH),aarch64)
ARCH := arm64
endif

# Test configuration
TEST_SUITE ?= smoke
PROCS ?= 1
TIMEOUT ?= 60m
REPORT_DIR ?= /tmp/allure-results
HARNESS_BUILD_ID ?= 0

EXTRA_FLAGS := -operator-registry=$(OPERATOR_REGISTRY) \
	-operator-repository=$(OPERATOR_REPOSITORY) \
	-operator-tag=$(OPERATOR_TAG) \
	-vm-vmsingledefault-image=$(VM_VMSINGLEDEFAULT_IMAGE) \
	-vm-vmsingledefault-version=$(VM_VMSINGLEDEFAULT_VERSION) \
	-vm-vmclusterdefault-vmselectdefault-image=$(VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE) \
	-vm-vmclusterdefault-vmselectdefault-version=$(VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION) \
	-vm-vmclusterdefault-vmstoragedefault-image=$(VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE) \
	-vm-vmclusterdefault-vmstoragedefault-version=$(VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION) \
	-vm-vmclusterdefault-vminsertdefault-image=$(VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE) \
	-vm-vmclusterdefault-vminsertdefault-version=$(VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION) \
	-vm-vmagentdefault-image=$(VM_VMAGENTDEFAULT_IMAGE) \
	-vm-vmagentdefault-version=$(VM_VMAGENTDEFAULT_VERSION) \
	-vm-vmalertdefault-image=$(VM_VMALERTDEFAULT_IMAGE) \
	-vm-vmalertdefault-version=$(VM_VMALERTDEFAULT_VERSION) \
	-vm-vmauthdefault-image=$(VM_VMAUTHDEFAULT_IMAGE) \
	-vm-vmauthdefault-version=$(VM_VMAUTHDEFAULT_VERSION) \
	-distributed-region=$(GCP_REGION) \
	-distributed-zones=$(DISTRIBUTED_ZONES)

ifneq ($(LICENSE_FILE),)
	EXTRA_FLAGS += --license-file=$(LICENSE_FILE)
endif

GINKGO_FLAGS := -procs=$(PROCS) \
	-timeout=$(TIMEOUT)
ifneq ($(VM_ENTERPRISE),)
	GINKGO_FLAGS += --label-filter='(enterprise||!enterprise)'
else
	GINKGO_FLAGS += --label-filter='!enterprise'
endif

ifneq ($(FLAKE_ATTEMPTS),)
	EXTRA_FLAGS += --ginkgo.flake-attempts=$(FLAKE_ATTEMPTS)
endif

# Targets
.PHONY: all
all: install-dependencies

.PHONY: install-dependencies
install-dependencies: install-go install-kubectl install-helm install-kind install-crust-gather install-vmexporter install-ginkgo

.PHONY: install-go
install-go:
	@mkdir -p $(BIN_DIR)
	@if [ ! -x $(BIN_DIR)/go ]; then \
		curl -LO https://go.dev/dl/go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		mkdir -p $(BIN_DIR)/.go; \
		tar -C $(BIN_DIR)/.go --strip-components=1 -xzf go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		rm go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		ln -sf $(BIN_DIR)/.go/bin/go $(BIN_DIR)/go; \
		ln -sf $(BIN_DIR)/.go/bin/gofmt $(BIN_DIR)/gofmt; \
	fi

.PHONY: install-kubectl
install-kubectl:
	@mkdir -p $(BIN_DIR)
	@if [ ! -f $(BIN_DIR)/kubectl ]; then \
		curl -LO "https://dl.k8s.io/release/$(KUBECTL_VERSION)/bin/$(OS)/$(ARCH)/kubectl"; \
		chmod +x kubectl; \
		mv kubectl $(BIN_DIR)/; \
	fi

.PHONY: install-helm
install-helm:
	@mkdir -p $(BIN_DIR)
	@if [ ! -f $(BIN_DIR)/helm ]; then \
		curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | HELM_INSTALL_DIR=$(BIN_DIR) bash -s -- --no-sudo; \
		$(BIN_DIR)/helm repo add vm https://victoriametrics.github.io/helm-charts/; \
		$(BIN_DIR)/helm repo add chaos-mesh https://charts.chaos-mesh.org; \
	fi
	$(BIN_DIR)/helm repo update

.PHONY: install-kind
install-kind:
	@mkdir -p $(BIN_DIR)
	$(call download-github-release,$(BIN_DIR)/kind,kubernetes-sigs/kind,$(KIND_VERSION),kind-$(OS)-$(ARCH),kind)

.PHONY: install-crust-gather
install-crust-gather:
	@mkdir -p $(BIN_DIR)
	$(call download-github-release,$(BIN_DIR)/kubectl-crust-gather,crust-gather/crust-gather,$(CRUST_GATHER_VERSION),kubectl-crust-gather_$(patsubst v%,%,$(CRUST_GATHER_VERSION))_$(OS)_$(ARCH).tar.gz,kubectl-crust-gather)

.PHONY: install-vmexporter
install-vmexporter:
	@mkdir -p $(BIN_DIR)
	$(call download-github-release,$(BIN_DIR)/vmexporter,VictoriaMetrics/vmgather,$(VMGATHER_VERSION),vmgather-$(VMGATHER_VERSION)-$(OS)-$(ARCH),vmgather)

.PHONY: install-ginkgo
install-ginkgo: install-go
	@if [ ! -f $(BIN_DIR)/ginkgo ]; then \
		GOBIN=$(BIN_DIR) go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION); \
	fi

.PHONY: install-ingress
install-ingress: install-kubectl
	$(BIN_DIR)/kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
	$(BIN_DIR)/kubectl delete -A ValidatingWebhookConfiguration ingress-nginx-admission || true
	# Wait for ingress to be ready
	$(BIN_DIR)/kubectl wait --namespace ingress-nginx \
	  --for=condition=ready pod \
	  --selector=app.kubernetes.io/component=controller \
	  --timeout=90s || true

# Unit tests
.PHONY: test-unit
test-unit:
	go mod download
	go test ./pkg/... -v -failfast

# Kind targets
.PHONY: kind-create
kind-create: install-kind
	$(BIN_DIR)/kind get clusters | grep -q kind || $(BIN_DIR)/kind create cluster --config manifests/kind.yaml

.PHONY: kind-delete
kind-delete:
	$(BIN_DIR)/kind delete cluster

.PHONY: test-kind
test-kind: install-dependencies kind-create
	$(MAKE) install-ingress
	@mkdir -p $(REPORT_DIR)/kind-smoke-test
	$(BIN_DIR)/ginkgo -v \
		-procs=1 \
		-timeout=60m \
		--label-filter=kind \
		./tests/smoke_test \
		-- \
		-env-k8s-distro=kind \
		$(EXTRA_FLAGS) \
		-report="$(REPORT_DIR)/kind-smoke-test"

# GKE targets
.PHONY: test-gke
test-gke: install-dependencies
	$(MAKE) gke-provision
	$(MAKE) gke-prepare-access
	$(MAKE) install-ingress
	$(MAKE) gke-run-test

.PHONY: gcloud-auth
gcloud-auth:
	@if [ -z "$(GOOGLE_APPLICATION_CREDENTIALS)" ]; then echo "GOOGLE_APPLICATION_CREDENTIALS is not set"; exit 1; fi
	gcloud auth activate-service-account --key-file="$(GOOGLE_APPLICATION_CREDENTIALS)"

.PHONY: gke-provision
gke-provision: gcloud-auth
	@if [ -z "$(PROJECT_ID)" ]; then echo "PROJECT_ID is not set"; exit 1; fi
	cd terraform/gke && \
		terraform init && \
		terraform apply -auto-approve -var="cluster_name=$(TEST_SUITE)-$(HARNESS_BUILD_ID)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)"

.PHONY: gke-prepare-access
gke-prepare-access: gcloud-auth
	@if [ -z "$(PROJECT_ID)" ]; then echo "PROJECT_ID is not set"; exit 1; fi
	gcloud container clusters get-credentials "$(TEST_SUITE)-$(HARNESS_BUILD_ID)" --region=$(GCP_REGION) --project="$(PROJECT_ID)"
	$(BIN_DIR)/kubectl -n kube-system create serviceaccount cluster-admin || true
	$(BIN_DIR)/kubectl create clusterrolebinding cluster-admin-binding --clusterrole=cluster-admin --serviceaccount=kube-system:cluster-admin || true
	# Generate dedicated kubeconfig for test
	$(BIN_DIR)/kubectl -n kube-system create token --duration=24h cluster-admin > /tmp/token.txt
	$(BIN_DIR)/kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > /tmp/ca.txt
	$(BIN_DIR)/kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}' > /tmp/server.txt

.PHONY: gke-run-test
gke-run-test:
	@mkdir -p $(REPORT_DIR)/$(TEST_SUITE)
	# Setup isolated kubeconfig if files exist
	if [ -f /tmp/token.txt ]; then \
		export KUBECONFIG=/tmp/kubeconfig.yaml; \
		$(BIN_DIR)/kubectl config set-cluster gke --server=$$(cat /tmp/server.txt) --certificate-authority=/tmp/ca.txt --embed-certs=true; \
		$(BIN_DIR)/kubectl config set-credentials cluster-admin --token=$$(cat /tmp/token.txt); \
		$(BIN_DIR)/kubectl config set-context production --cluster gke --user cluster-admin; \
		$(BIN_DIR)/kubectl config use-context production; \
	fi; \
	$(BIN_DIR)/ginkgo -v \
	    $(GINKGO_FLAGS) \
		"./tests/$(TEST_SUITE)_test" \
		-- \
		-env-k8s-distro=gke \
		$(EXTRA_FLAGS) \
		-report="$(REPORT_DIR)/$(TEST_SUITE)"

.PHONY: clean-gke
clean-gke: gcloud-auth
	cd terraform/gke && \
		terraform init && \
		terraform destroy -auto-approve -var="cluster_name=$(TEST_SUITE)-$(HARNESS_BUILD_ID)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)"
	# Disk cleanup
	@echo "Cleaning up unused disks in $(GCP_REGION)..."
	@for zone_suffix in a b c; do \
		ZONE="$(GCP_REGION)$$zone_suffix"; \
		echo "Checking zone $$ZONE..."; \
		UNUSED_DISKS=$$(gcloud compute disks list --filter="-users:*" --format "value(name)" --zones="$$ZONE" 2>/dev/null || true); \
		if [ -n "$$UNUSED_DISKS" ]; then \
			echo "Deleting unused disks in $$ZONE: $$UNUSED_DISKS"; \
			echo "$$UNUSED_DISKS" | xargs -r gcloud compute disks delete --quiet --zone="$$ZONE" || true; \
		else \
			echo "No unused disks found in $$ZONE."; \
		fi; \
	done

# download-github-release will download a binary from github releases
# $1 - target path with name of binary
# $2 - repo url
# $3 - specific version of package
# $4 - artifact name
# $5 - binary name
define download-github-release
@[ -f $(1) ] || { \
set -e; \
url="https://github.com/$(2)/releases/download/$(3)/$(4)"; \
echo "Downloading $(1) from $${url}" ;\
if echo "$(4)" | grep -q ".tar.gz$$"; then \
curl -sL $${url} -o $(BIN_DIR)/$(4); \
tar -xzf $(BIN_DIR)/$(4) -C $(BIN_DIR); \
if [ "$(BIN_DIR)/$(5)" != "$(1)" ]; then mv $(BIN_DIR)/$(5) $(1); fi; \
rm $(BIN_DIR)/$(4); \
else \
curl -sL $${url} -o $(1); \
chmod +x $(1); \
fi; \
}
endef
