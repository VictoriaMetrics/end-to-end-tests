# Makefile for VictoriaMetrics End-to-End Tests

# Dependencies versions
GO_VERSION ?= 1.27.0
KIND_VERSION ?= v0.33.0
KUBECTL_VERSION ?= v1.37.0
CRUST_GATHER_VERSION ?= v0.17.1
K8S_VERSION ?= 1.36
ARGOCD_VERSION ?= v3.5.2
VMGATHER_VERSION ?= v1.11.0
GINKGO_VERSION ?= latest
OPENTOFU_VERSION ?= 1.12.6
TRAEFIK_CHART_VERSION ?= 41.3.0
K6_OPERATOR_VERSION ?= v1.6.0
GATEWAY_API_VERSION ?= v1.4.1

# Image versions
VM_K8S_STACK_CHART_VERSION = 0.91.2
VM_DISTRIBUTED_CHART_VERSION = 0.44.0
VL_SINGLE_CHART_VERSION = 0.13.9
VL_COLLECTOR_CHART_VERSION = 0.3.7
VL_VERSION ?= v1.52.0
VL_ENTERPRISE_VERSION ?= v1.52.0-enterprise

OPERATOR_REGISTRY ?= quay.io
OPERATOR_REPOSITORY ?= victoriametrics/operator
OPERATOR_TAG ?= v0.74.1
OPERATOR_CHART_VERSION ?= 0.67.2

VM_SINGLEDEFAULT_VERSION ?= v1.150.0
VM_CLUSTERDEFAULT_VERSION ?= v1.150.0-cluster

# Enterprise versions
ifneq ($(VM_ENTERPRISE),)
VM_CLUSTER_ENTERPRISE_VERSION := v1.150.0-enterprise-cluster
VM_SINGLE_ENTERPRISE_VERSION := v1.150.0-enterprise

VM_SINGLEDEFAULT_VERSION := $(VM_SINGLE_ENTERPRISE_VERSION)
VM_CLUSTERDEFAULT_VERSION := $(VM_CLUSTER_ENTERPRISE_VERSION)
endif

# Release candidate versions
ifneq ($(VM_RC),)
VM_CLUSTER_RC_VERSION := v1.150.0-cluster-rc0
VM_SINGLE_RC_VERSION := v1.144.0-rc0

VM_SINGLEDEFAULT_VERSION := $(VM_SINGLE_RC_VERSION)
VM_CLUSTERDEFAULT_VERSION := $(VM_CLUSTER_RC_VERSION)
endif

# LTS versions
ifeq ($(VM_LTS_VERSION),current)
VM_CLUSTER_CURRENT_LTS_VERSION := v1.136.16-enterprise-cluster
VM_SINGLE_CURRENT_LTS_VERSION := v1.136.10-enterprise

VM_SINGLEDEFAULT_VERSION := $(VM_SINGLE_CURRENT_LTS_VERSION)
VM_CLUSTERDEFAULT_VERSION := $(VM_CLUSTER_CURRENT_LTS_VERSION)
endif

ifeq ($(VM_LTS_VERSION),previous)
VM_CLUSTER_PREVIOUS_LTS_VERSION := v1.122.27-enterprise-cluster
VM_SINGLE_PREVIOUS_LTS_VERSION := v1.122.23-enterprise

VM_SINGLEDEFAULT_VERSION := $(VM_SINGLE_PREVIOUS_LTS_VERSION)
VM_CLUSTERDEFAULT_VERSION := $(VM_CLUSTER_PREVIOUS_LTS_VERSION)
endif

# Operator LTS versions (0.68.x series)
ifeq ($(OPERATOR_LTS_VERSION),current)
OPERATOR_TAG := v0.68.7
endif

# Operator release candidate version
ifneq ($(OPERATOR_RC),)
OPERATOR_TAG := v0.68.3-rc0
endif

VM_VMSINGLEDEFAULT_IMAGE ?= quay.io/victoriametrics/victoria-metrics
VM_VMSINGLEDEFAULT_VERSION ?= $(VM_SINGLEDEFAULT_VERSION)

VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE ?= quay.io/victoriametrics/vmselect
VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION ?= $(VM_CLUSTERDEFAULT_VERSION)

VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE ?= quay.io/victoriametrics/vmstorage
VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION ?= $(VM_CLUSTERDEFAULT_VERSION)

VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE ?= quay.io/victoriametrics/vminsert
VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION ?= $(VM_CLUSTERDEFAULT_VERSION)

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

# GCS / Allure report configuration
GCS_BUCKET ?= vrutkovs-e2e-results
REPORT_SUITE ?= $(TEST_SUITE)$(if $(K8S_VERSION),-k8s-$(subst .,-,$(K8S_VERSION)))
ALLURE_RESULTS_DIR ?= ./allure-results
ALLURE_REPORT_DIR ?= $(CURDIR)/report
PR_REPORT_DIR ?= /tmp/report

OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m)

ifeq ($(ARCH),x86_64)
ARCH := amd64
endif
ifeq ($(ARCH),aarch64)
ARCH := arm64
endif

# Test configuration
# TEST_BINARY: path to a precompiled test binary (e.g. /tests/vm_load_test.test).
# When set, TEST_SUITE is derived automatically from the binary name.
# When not set, TEST_SUITE must be provided and the binary is resolved as
# /tests/$(TEST_SUITE)_test.test.
TEST_BINARY ?=
TEST_SUITE ?= $(if $(TEST_BINARY),$(patsubst %_test.test,%,$(notdir $(TEST_BINARY))),vm-functional)
MONITORING_MIN_NODE_COUNT ?= 2
MONITORING_MAX_NODE_COUNT ?= 8
ifeq ($(TEST_SUITE),operator)
MONITORING_MIN_NODE_COUNT := 0
endif
MANIFESTS_DIR ?= /app/manifests
PROCS ?= 1
TIMEOUT ?= 60m
REPORT_DIR ?= /tmp/allure-results
BUILD_ID ?= 0

# Unique identifiers for parallel execution on shared hosts (e.g. self-hosted runners sharing /tmp)
export CLUSTER_ID := $(TEST_SUITE)-$(BUILD_ID)-$(subst .,-,$(K8S_VERSION))
KUBECONFIG_FILE := /tmp/kubeconfig-$(CLUSTER_ID).yaml
TOKEN_FILE := /tmp/token-$(CLUSTER_ID).txt
CA_FILE := /tmp/ca-$(CLUSTER_ID).txt
SERVER_FILE := /tmp/server-$(CLUSTER_ID).txt
INGRESS_IP_FILE := /tmp/ingress-ip-$(CLUSTER_ID).txt

EXTRA_FLAGS := -operator-registry=$(OPERATOR_REGISTRY) \
	-operator-repository=$(OPERATOR_REPOSITORY) \
	-operator-tag=$(OPERATOR_TAG) \
	-operator-chart-version=$(OPERATOR_CHART_VERSION) \
	-argocd-version=$(ARGOCD_VERSION) \
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
	-distributed-zones=$(DISTRIBUTED_ZONES) \
	-vm-k8s-stack-chart-version=$(VM_K8S_STACK_CHART_VERSION) \
	-vm-distributed-chart-version=$(VM_DISTRIBUTED_CHART_VERSION) \
	-vl-single-chart-version=$(VL_SINGLE_CHART_VERSION) \
	-vl-collector-chart-version=$(VL_COLLECTOR_CHART_VERSION) \
	-vl-version=$(VL_VERSION)
	-vl-enterprise-version=$(VL_ENTERPRISE_VERSION)

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

# Targets
.PHONY: all
all: install-dependencies

.PHONY: install-dependencies
install-dependencies: install-go install-kubectl install-helm install-kind install-crust-gather install-ginkgo

.PHONY: install-go
install-go:
	mkdir -p $(BIN_DIR)
	if [ ! -x $(BIN_DIR)/go ] && ! command -v go >/dev/null 2>&1; then \
		curl -LO https://go.dev/dl/go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		mkdir -p $(BIN_DIR)/.go; \
		tar -C $(BIN_DIR)/.go --strip-components=1 -xzf go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		rm go$(GO_VERSION).$(OS)-$(ARCH).tar.gz; \
		ln -sf $(BIN_DIR)/.go/bin/go $(BIN_DIR)/go; \
		ln -sf $(BIN_DIR)/.go/bin/gofmt $(BIN_DIR)/gofmt; \
	fi

.PHONY: install-kubectl
install-kubectl:
	mkdir -p $(BIN_DIR)
	if [ ! -f $(BIN_DIR)/kubectl ] && ! command -v kubectl >/dev/null 2>&1; then \
		curl -LO "https://dl.k8s.io/release/$(KUBECTL_VERSION)/bin/$(OS)/$(ARCH)/kubectl"; \
		chmod +x kubectl; \
		mv kubectl $(BIN_DIR)/; \
	fi

.PHONY: install-helm
install-helm:
	mkdir -p $(BIN_DIR)
	if [ ! -f $(BIN_DIR)/helm ] && ! command -v helm >/dev/null 2>&1; then \
		curl https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | HELM_INSTALL_DIR=$(BIN_DIR) bash -s -- --no-sudo; \
		helm repo add vm https://victoriametrics.github.io/helm-charts/; \
		helm repo add chaos-mesh https://charts.chaos-mesh.org; \
		helm repo add strimzi https://strimzi.io/charts/; \
		helm repo add kedacore https://kedacore.github.io/charts; \
		helm repo add traefik https://traefik.github.io/charts; \
		helm repo update; \
	fi

.PHONY: install-kind
install-kind:
	mkdir -p $(BIN_DIR)
	$(call download-github-release,$(BIN_DIR)/kind,kubernetes-sigs/kind,$(KIND_VERSION),kind-$(OS)-$(ARCH),kind)

.PHONY: install-crust-gather
install-crust-gather:
	mkdir -p $(BIN_DIR)
	$(call download-github-release,$(BIN_DIR)/kubectl-crust-gather,crust-gather/crust-gather,$(CRUST_GATHER_VERSION),kubectl-crust-gather_$(patsubst v%,%,$(CRUST_GATHER_VERSION))_$(OS)_$(ARCH).tar.gz,kubectl-crust-gather)

.PHONY: install-ginkgo
install-ginkgo: install-go
	if [ ! -f $(BIN_DIR)/ginkgo ] && ! command -v ginkgo >/dev/null 2>&1; then \
		GOBIN=$(BIN_DIR) go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION); \
	fi

.PHONY: install-ingress
install-ingress: install-kubectl install-helm
	# kind doesn't ship Traefik built-in (unlike k3s), so install the chart
	# directly. ports.*.hostPort binds the pod's containerPort onto the kind
	# node's own network namespace (the same hostPort mechanism
	# ingress-nginx's own kind-specific manifest used), which kind.yaml's
	# extraPortMappings then forwards from the Docker host's port 80 to.
	# nodeSelector pins the pod to the node kind.yaml labels "ingress-ready";
	# the toleration is only needed by manifests/kind/kind-distributed.yaml,
	# where that node is the (tainted) control-plane node.
	helm upgrade --install traefik traefik/traefik \
	  --namespace kube-system --create-namespace \
	  --version $(TRAEFIK_CHART_VERSION) \
	  --set ports.web.hostPort=80 \
	  --set ports.websecure.hostPort=443 \
	  --set nodeSelector."ingress-ready"=true \
	  --set tolerations[0].key=node-role.kubernetes.io/control-plane \
	  --set tolerations[0].operator=Exists \
	  --set tolerations[0].effect=NoSchedule
	# Wait for ingress to be ready
	kubectl wait --namespace kube-system \
	  --for=condition=ready pod \
	  --selector=app.kubernetes.io/name=traefik \
	  --timeout=180s || true

.PHONY: install-ingress-k3s
install-ingress-k3s: install-kubectl
	# Traefik ships built into k3s (kube-system/traefik), pinned to a single
	# instance on the control-plane node via the HelmChartConfig k3s's server
	# startup script drops into /var/lib/rancher/k3s/server/manifests (see
	# terraform/k3s/templates/k3s-server.sh.tpl) — nothing to install here.
	kubectl rollout status --namespace kube-system \
	  deployment/traefik \
	  --timeout=300s
	# k3s ships its own "servicelb" (Klipper) controller, which fulfils
	# LoadBalancer Services without a cloud load balancer by binding the
	# node's external IP (--node-external-ip, set at provisioning time).
	for i in $$(seq 1 120); do \
	  INGRESS_LB_IP=$$(kubectl get svc traefik -n kube-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}'); \
	  if [ -n "$$INGRESS_LB_IP" ]; then \
	    echo "$$INGRESS_LB_IP" > $(INGRESS_IP_FILE); \
	    break; \
	  fi; \
	  sleep 5; \
	done; \
	if [ ! -s $(INGRESS_IP_FILE) ]; then \
	  echo "traefik LoadBalancer did not receive an external IP"; \
	  kubectl get svc traefik -n kube-system -o wide; \
	  kubectl describe svc traefik -n kube-system; \
	  kubectl get events -n kube-system --sort-by=.lastTimestamp; \
	  exit 1; \
	fi
	# Wait for the external forwarding rule/backend to accept TCP connections
	for i in $$(seq 1 60); do \
	  INGRESS_LB_IP=$$(cat $(INGRESS_IP_FILE)); \
	  if curl --connect-timeout 5 --max-time 10 --silent --show-error --output /dev/null "http://$$INGRESS_LB_IP"; then \
	    exit 0; \
	  fi; \
	  sleep 5; \
	done; \
	echo "traefik LoadBalancer $$(cat $(INGRESS_IP_FILE)):80 did not become reachable"; \
	exit 1

.PHONY: install-ingress-gke
install-ingress-gke: install-kubectl install-helm
	helm upgrade --install traefik traefik/traefik \
	  --namespace kube-system --create-namespace \
	  --version $(TRAEFIK_CHART_VERSION)
	# default-nodes is preemptible: a single controller replica means a single
	# node preemption zeroes out the LB backend pool for the rest of the run
	# (the ingress host is only discovered once and never re-verified). Run 2
	# replicas spread across distinct nodes so one preemption can't take down
	# the whole ingress.
	kubectl -n kube-system patch deployment traefik \
	  --type=strategic -p='{"spec":{"replicas":2,"template":{"spec":{"affinity":{"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[{"labelSelector":{"matchLabels":{"app.kubernetes.io/name":"traefik"}},"topologyKey":"kubernetes.io/hostname"}]}}}}}}'
	kubectl rollout status --namespace kube-system \
	  deployment/traefik \
	  --timeout=300s
	# Wait for GKE to assign an ephemeral IP to the LoadBalancer Service.
	# Forwarding rule provisioning can take longer than 5 minutes on fresh clusters.
	for i in $$(seq 1 120); do \
	  INGRESS_LB_IP=$$(kubectl get svc traefik -n kube-system -o jsonpath='{.status.loadBalancer.ingress[0].ip}'); \
	  if [ -n "$$INGRESS_LB_IP" ]; then \
	    echo "$$INGRESS_LB_IP" > $(INGRESS_IP_FILE); \
	    break; \
	  fi; \
	  sleep 5; \
	done; \
	if [ ! -s $(INGRESS_IP_FILE) ]; then \
	  echo "traefik LoadBalancer did not receive an external IP"; \
	  kubectl get svc traefik -n kube-system -o wide; \
	  kubectl describe svc traefik -n kube-system; \
	  kubectl get events -n kube-system --sort-by=.lastTimestamp; \
	  exit 1; \
	fi
	# Wait for the external forwarding rule/backend to accept TCP connections
	for i in $$(seq 1 60); do \
	  INGRESS_LB_IP=$$(cat $(INGRESS_IP_FILE)); \
	  if curl --connect-timeout 5 --max-time 10 --silent --show-error --output /dev/null "http://$$INGRESS_LB_IP"; then \
	    exit 0; \
	  fi; \
	  sleep 5; \
	done; \
	echo "traefik LoadBalancer $$(cat $(INGRESS_IP_FILE)):80 did not become reachable"; \
	exit 1

# Unit tests
.PHONY: test-unit
test-unit: install-go
	go mod download
	go test ./pkg/... -v -failfast

# Kind targets
.PHONY: kind-create
kind-create: install-kind
	kind get clusters | grep -q "^$(CLUSTER_ID)$$" || \
		kind create cluster --name $(CLUSTER_ID) --config manifests/kind/kind.yaml
	kind export kubeconfig --name $(CLUSTER_ID) --kubeconfig $(KUBECONFIG_FILE)

.PHONY: kind-delete
kind-delete:
	kind delete cluster --name $(CLUSTER_ID)
	rm -f $(KUBECONFIG_FILE)

.PHONY: test-kind
test-kind: install-dependencies kind-create
	KUBECONFIG=$(KUBECONFIG_FILE) $(MAKE) install-ingress
	mkdir -p $(REPORT_DIR)/kind-$(TEST_SUITE)-test
	KUBECONFIG=$(KUBECONFIG_FILE) ginkgo -v \
		-procs=1 \
		-timeout=60m \
		./tests/$(TEST_SUITE)_test \
		-- \
		-env-k8s-distro=kind \
		$(EXTRA_FLAGS) \
		-report="$(REPORT_DIR)/kind-$(TEST_SUITE)-test"

.PHONY: test-kind-enterprise
test-kind-enterprise: install-dependencies kind-create
	KUBECONFIG=$(KUBECONFIG_FILE) $(MAKE) install-ingress
	mkdir -p $(REPORT_DIR)/kind-enterprise-test
	KUBECONFIG=$(KUBECONFIG_FILE) ginkgo -v \
		-procs=1 \
		-timeout=60m \
		--label-filter='enterprise||!enterprise' \
		./tests/vm-enterprise_test \
		-- \
		-env-k8s-distro=kind \
		$(EXTRA_FLAGS) \
		$(if $(LICENSE_FILE),--license-file=$(LICENSE_FILE),) \
		-report="$(REPORT_DIR)/kind-enterprise-test"

# GKE / k3s targets
#
# TEST_SUITE=operator uses k3s (terraform/k3s) so its K8S_VERSION matrix can
# cover Kubernetes releases GKE no longer supports (see K8S_VERSIONS in
# .buildkite/generate_pipeline.py). Every other suite uses standard GKE
# (terraform/gke). Both install Traefik as the ingress controller.
.PHONY: test-gke
test-gke: install-dependencies
ifeq ($(TEST_SUITE),operator)
	$(MAKE) k3s-provision
	$(MAKE) k3s-prepare-access
	KUBECONFIG=$(KUBECONFIG_FILE) $(MAKE) install-ingress-k3s
	$(MAKE) k3s-run-test
else
	$(MAKE) gke-provision
	$(MAKE) gke-prepare-access
	KUBECONFIG=$(KUBECONFIG_FILE) $(MAKE) install-ingress-gke
	$(MAKE) gke-run-test
endif

.PHONY: gcloud-auth
gcloud-auth:
	if [ -z "$(GOOGLE_APPLICATION_CREDENTIALS)" ]; then echo "GOOGLE_APPLICATION_CREDENTIALS is not set"; exit 1; fi
	gcloud auth activate-service-account --key-file="$(GOOGLE_APPLICATION_CREDENTIALS)"

.PHONY: gke-provision
gke-provision: gcloud-auth
	if [ -z "$(PROJECT_ID)" ]; then echo "PROJECT_ID is not set"; exit 1; fi
	cd terraform/gke && \
		tofu init && \
		tofu apply -auto-approve -state=/tmp/terraform-$(CLUSTER_ID).tfstate -var="cluster_name=$(CLUSTER_ID)" -var="k8s_version=$(K8S_VERSION)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)" -var="monitoring_min_node_count=$(MONITORING_MIN_NODE_COUNT)" -var="monitoring_max_node_count=$(MONITORING_MAX_NODE_COUNT)"

.PHONY: gke-prepare-access
gke-prepare-access: gcloud-auth
	if [ -z "$(PROJECT_ID)" ]; then echo "PROJECT_ID is not set"; exit 1; fi
	gcloud container clusters get-credentials "$(CLUSTER_ID)" --region=$(GCP_REGION) --project="$(PROJECT_ID)"
	kubectl -n kube-system create serviceaccount cluster-admin || true
	kubectl create clusterrolebinding cluster-admin-binding --clusterrole=cluster-admin --serviceaccount=kube-system:cluster-admin || true
	# Generate dedicated kubeconfig for test using paths unique to this cluster
	kubectl -n kube-system create token --duration=24h cluster-admin > $(TOKEN_FILE)
	kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}' | base64 -d > $(CA_FILE)
	kubectl config view --raw --minify -o jsonpath='{.clusters[0].cluster.server}' > $(SERVER_FILE)
	export KUBECONFIG=$(KUBECONFIG_FILE); \
	kubectl config set-cluster gke --server=$$(cat $(SERVER_FILE)) --certificate-authority=$(CA_FILE) --embed-certs=true; \
	kubectl config set-credentials cluster-admin --token=$$(cat $(TOKEN_FILE)); \
	kubectl config set-context production --cluster gke --user cluster-admin; \
	kubectl config use-context production

.PHONY: gke-run-test
gke-run-test:
	mkdir -p $(REPORT_DIR)/$(REPORT_SUITE)
	MDX_PASSWORD=$(MDX_PASSWORD) KUBECONFIG=$(KUBECONFIG_FILE) ginkgo -v \
	    $(GINKGO_FLAGS) \
		$(or $(TEST_BINARY),./tests/$(TEST_SUITE)_test) \
		-- \
		-env-k8s-distro=gke \
		-manifests-dir=$(MANIFESTS_DIR) \
		-ingress-host=$$(cat $(INGRESS_IP_FILE)) \
		$(EXTRA_FLAGS) \
		-report="$(REPORT_DIR)/$(REPORT_SUITE)"

.PHONY: k3s-provision
k3s-provision: gcloud-auth
	if [ -z "$(PROJECT_ID)" ]; then echo "PROJECT_ID is not set"; exit 1; fi
	cd terraform/k3s && \
		tofu init && \
		tofu apply -auto-approve -state=/tmp/terraform-$(CLUSTER_ID).tfstate -var="cluster_name=$(CLUSTER_ID)" -var="k8s_version=$(K8S_VERSION)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)" -var="monitoring_node_count=$(MONITORING_MIN_NODE_COUNT)"

.PHONY: k3s-prepare-access
k3s-prepare-access:
	cd terraform/k3s && \
		tofu output -state=/tmp/terraform-$(CLUSTER_ID).tfstate -raw kubeconfig > $(KUBECONFIG_FILE)
	KUBECONFIG=$(KUBECONFIG_FILE) kubectl cluster-info

.PHONY: k3s-run-test
k3s-run-test:
	mkdir -p $(REPORT_DIR)/$(REPORT_SUITE)
	MDX_PASSWORD=$(MDX_PASSWORD) KUBECONFIG=$(KUBECONFIG_FILE) ginkgo -v \
	    $(GINKGO_FLAGS) \
		$(or $(TEST_BINARY),./tests/$(TEST_SUITE)_test) \
		-- \
		-env-k8s-distro=k3s \
		-manifests-dir=$(MANIFESTS_DIR) \
		-ingress-host=$$(cat $(INGRESS_IP_FILE)) \
		$(EXTRA_FLAGS) \
		-report="$(REPORT_DIR)/$(REPORT_SUITE)"

.PHONY: clean-gke
clean-gke: gcloud-auth
ifeq ($(TEST_SUITE),operator)
	# Ensure these gitignored files exist so destroy's data.local_file reads don't fail on a fresh checkout that never ran apply.
	touch terraform/k3s/.node-token-$(CLUSTER_ID) terraform/k3s/.kubeconfig-$(CLUSTER_ID)
	cd terraform/k3s && \
		tofu init && \
		tofu destroy -auto-approve -state=/tmp/terraform-$(CLUSTER_ID).tfstate -var="cluster_name=$(CLUSTER_ID)" -var="k8s_version=$(K8S_VERSION)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)"
	rm -f terraform/k3s/.node-token-$(CLUSTER_ID) terraform/k3s/.kubeconfig-$(CLUSTER_ID) terraform/k3s/.ssh-$(CLUSTER_ID)
else
	cd terraform/gke && \
		tofu init && \
		tofu destroy -auto-approve -state=/tmp/terraform-$(CLUSTER_ID).tfstate -var="cluster_name=$(CLUSTER_ID)" -var="k8s_version=$(K8S_VERSION)" -var="region=$(GCP_REGION)" -var="project_id=$(PROJECT_ID)"
endif
	rm -f $(TOKEN_FILE) $(CA_FILE) $(SERVER_FILE) $(KUBECONFIG_FILE) $(INGRESS_IP_FILE) /tmp/terraform-$(CLUSTER_ID).tfstate /tmp/terraform-$(CLUSTER_ID).tfstate.backup
	# Disk cleanup
	# Scoped to this cluster's own disks (goog-k8s-cluster-name label) only.
	# Other test suites run concurrently in dedicated clusters in the same
	# project/region; an unscoped sweep can delete a disk another suite just
	# provisioned but hasn't attached yet, since it briefly has no users.
	echo "Cleaning up unused disks for cluster $(CLUSTER_ID) in $(GCP_REGION)..."
	for zone_suffix in a b c; do \
		ZONE="$(GCP_REGION)-$$zone_suffix"; \
		echo "Checking zone $$ZONE..."; \
		UNUSED_DISKS=$$(gcloud compute disks list --filter="labels.goog-k8s-cluster-name=$(CLUSTER_ID)" --format="value(name,users)" --zones="$$ZONE" 2>/dev/null | awk -F'\t' 'NF<2 || $$2==""{ print $$1 }' || true); \
		if [ -n "$$UNUSED_DISKS" ]; then \
			echo "Deleting unused disks in $$ZONE: $$UNUSED_DISKS"; \
			echo "$$UNUSED_DISKS" | xargs -r gcloud compute disks delete --quiet --zone="$$ZONE" || true; \
		else \
			echo "No unused disks found in $$ZONE."; \
		fi; \
	done

# Upload allure results for the current TEST_SUITE to GCS.
# Requires BUILD_ID and GOOGLE_APPLICATION_CREDENTIALS to be set.
.PHONY: upload-results
upload-results:
	if [ -d "$(REPORT_DIR)/$(REPORT_SUITE)" ]; then \
		gcloud storage cp -r "$(REPORT_DIR)/$(REPORT_SUITE)" \
		"gs://$(GCS_BUCKET)/allure-results/$(BUILD_ID)/$(REPORT_SUITE)"; \
	else \
		echo "No results found at $(REPORT_DIR)/$(REPORT_SUITE), skipping upload"; \
	fi

# Generate an Allure report for a PR build from locally available suite results.
# Expects suite results to already be present under $(ALLURE_RESULTS_DIR)/.
# Injects history from GCS for trend graphs, generates the HTML report into
# $(PR_REPORT_DIR) for Buildkite artifact upload.
# Does NOT upload to GCS or save history.
# Requires GOOGLE_APPLICATION_CREDENTIALS to be set (for history fetch).
.PHONY: generate-pr-report
generate-pr-report:
	mkdir -p $(ALLURE_RESULTS_DIR)
	python3 scripts/merge_suites.py \
		$(ALLURE_RESULTS_DIR) $(ALLURE_RESULTS_DIR)/merged \
		|| exit 0; \
	npx --yes allure@3 generate --cwd $(ALLURE_RESULTS_DIR)/merged -o $(PR_REPORT_DIR); \
	cd $$(dirname $(PR_REPORT_DIR)) && tar czf $$(basename $(PR_REPORT_DIR)).tar.gz $$(basename $(PR_REPORT_DIR))

# Download all suite results, generate a single combined Allure report, and publish to GCS.
# For main branch builds, all available build directories under allure-results/ in GCS are
# listed, sorted alphabetically, and the last 10 are downloaded so Allure shows richer historical data.
# Allure history is injected before generation so trend/retry graphs are populated from
# previous runs. After generation the new history is saved back for the next run.
# Requires BUILD_ID, BUILDKITE_BRANCH, and GOOGLE_APPLICATION_CREDENTIALS to be set.
.PHONY: deploy-report
deploy-report:
	mkdir -p $(ALLURE_RESULTS_DIR)
	gcloud storage ls "gs://$(GCS_BUCKET)/allure-results/" 2>/dev/null \
		| sort -V | grep -v "history.jsonl" | tail -1 \
		| while read -r d; do \
			bid=$$(basename "$$d"); \
			echo "fetching info for build $$bid"; \
			mkdir -p "$(ALLURE_RESULTS_DIR)/$$bid"; \
			gcloud storage ls -r "$$d" 2>/dev/null \
				| grep -E '^gs://' \
				| grep -v ':$$' \
				| gcloud storage cp -n --read-paths-from-stdin "$(ALLURE_RESULTS_DIR)/$$bid/" || true; \
			tmp="$(ALLURE_RESULTS_DIR)/_tmp_$$bid"; \
			python3 scripts/merge_suites.py "$(ALLURE_RESULTS_DIR)/$$bid" "$$tmp" 2>/dev/null && \
				rm -rf "$(ALLURE_RESULTS_DIR)/$$bid" && \
				mv "$$tmp" "$(ALLURE_RESULTS_DIR)" || true; \
		done
	python3 scripts/merge_suites.py \
		$(ALLURE_RESULTS_DIR) $(ALLURE_RESULTS_DIR)/merged \
		|| exit 0;
	gcloud storage cp "gs://$(GCS_BUCKET)/allure-results/history.jsonl" "$(ALLURE_RESULTS_DIR)/merged" \
		|| exit 0;
	echo "{\"historyPath\": \"$(ALLURE_RESULTS_DIR)/merged/history.jsonl\"}" > $(ALLURE_RESULTS_DIR)/merged/allurerc.json;
	npx --yes allure@3 generate --cwd "$(ALLURE_RESULTS_DIR)/merged" -o $(ALLURE_REPORT_DIR);
	gcloud storage cp "$(ALLURE_RESULTS_DIR)/merged/history.jsonl" \
		"gs://$(GCS_BUCKET)/allure-results/history.jsonl";
	gcloud storage cp -r $(ALLURE_REPORT_DIR)/ "gs://$(GCS_BUCKET)/"

# download-github-release will download a binary from github releases
# $1 - target path with name of binary
# $2 - repo url
# $3 - specific version of package
# $4 - artifact name
# $5 - binary name
define download-github-release
@[ -f $(1) ] || command -v $(5) >/dev/null 2>&1 || { \
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
