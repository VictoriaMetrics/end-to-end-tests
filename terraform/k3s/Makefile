# Makefile for k3s cluster with Traefik Ingress Controller (OpenTofu)

.PHONY: help init plan cluster destroy clean kubectl check-auth install-%

# Variables
TERRAFORM := tofu
KUBECTL := kubectl
GCLOUD := gcloud

K8S_TYPE := k3s
MANIFESTS_DIR := ../manifests
KUBECONFIG_FILE := /tmp/kubeconfig-k3s-demo.yaml

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
NC := \033[0m # No Color

all: cluster kubectl install-traefik install-grafana install-victoriametrics install-chaos-mesh

# Default target
help: ## Display this help message
	@echo -e "$(GREEN)k3s Cluster with VictoriaMetrics demo$(NC)"
	@echo ""
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "$(YELLOW)%-30s$(NC) %s\n", $$1, $$2}'

## Check GCP authentication
check-auth: ## Check GCP authentication status
	@echo -e "$(YELLOW)Checking GCP authentication...$(NC)"
	@if gcloud auth application-default print-access-token > /dev/null 2>&1; then \
		echo -e "$(GREEN)✓ GCP authentication configured$(NC)"; \
		echo "Project: $$(gcloud config get-value project)"; \
	else \
		echo -e "$(RED)✗ Not authenticated. Running: gcloud auth application-default login$(NC)"; \
		gcloud auth application-default login; \
	fi

init: check-auth ## Initialize OpenTofu
	@echo -e "$(YELLOW)Initializing OpenTofu...$(NC)"
	@$(TERRAFORM) init
	@echo -e "$(GREEN)✓ OpenTofu initialized$(NC)"

plan: init ## Run OpenTofu plan
	@echo -e "$(YELLOW)Running OpenTofu plan...$(NC)"
	@$(TERRAFORM) plan

cluster: init ## Apply OpenTofu configuration
	@echo -e "$(YELLOW)Applying OpenTofu configuration...$(NC)"
	@echo -e "$(RED)This will create GCP resources and incur costs!$(NC)"
	@$(TERRAFORM) apply -auto-approve
	@echo -e "$(GREEN)✓ Infrastructure deployed$(NC)"

destroy: ## Destroy all k3s cluster resources
	@echo -e "$(RED)WARNING: This will destroy all resources!$(NC)"
	@$(TERRAFORM) destroy -auto-approve
	@echo -e "$(GREEN)✓ Resources destroyed$(NC)"

kubectl: ## Write a kubeconfig for the k3s cluster and point kubectl at it
	@echo -e "$(YELLOW)Configuring kubectl...$(NC)"
	@if [ -f terraform.tfstate ]; then \
		$(TERRAFORM) output -raw kubeconfig > $(KUBECONFIG_FILE) && \
		echo -e "$(GREEN)✓ kubeconfig written to $(KUBECONFIG_FILE)$(NC)" && \
		KUBECONFIG=$(KUBECONFIG_FILE) $(KUBECTL) cluster-info; \
	else \
		echo -e "$(RED)✗ No terraform.tfstate found. Run 'make cluster' first$(NC)"; \
	fi

clean: ## Clean local OpenTofu files
	@echo -e "$(YELLOW)Cleaning local OpenTofu files...$(NC)"
	@rm -rf .terraform terraform.tfstate terraform.tfstate.backup .terraform.lock.hcl
	@echo -e "$(GREEN)✓ Cleaned$(NC)"

install-%: ## Install manifests
	$(MAKE) -C $(MANIFESTS_DIR) install-$*
