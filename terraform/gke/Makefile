# Makefile for GKE cluster with NGINX Ingress Controller

.PHONY: help init plan cluster destroy clean kubectl check-auth install-%

# Variables
TERRAFORM := terraform
KUBECTL := kubectl
GCLOUD := gcloud

K8S_TYPE := gke
MANIFESTS_DIR := ../manifests

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[1;33m
NC := \033[0m # No Color

all: cluster kubectl install-nginx install-grafana install-victoriametrics install-chaos-mesh

# Default target
help: ## Display this help message
	@echo -e "$(GREEN)GKE Cluster with VictoriaMetrics demo$(NC)"
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

init: check-auth ## Initialize Terraform
	@echo -e "$(YELLOW)Initializing Terraform...$(NC)"
	@$(TERRAFORM) init
	@echo -e "$(GREEN)✓ Terraform initialized$(NC)"

plan: init ## Run Terraform plan
	@echo -e "$(YELLOW)Running Terraform plan...$(NC)"
	@$(TERRAFORM) plan

cluster: init ## Apply Terraform configuration
	@echo -e "$(YELLOW)Applying Terraform configuration...$(NC)"
	@echo -e "$(RED)This will create GCP resources and incur costs!$(NC)"
	@echo -e "$(YELLOW)Note: Using preemptible nodes to reduce costs$(NC)"
	@$(TERRAFORM) apply -auto-approve
	@echo -e "$(GREEN)✓ Infrastructure deployed$(NC)"

destroy: ## Destroy all GKE resources
	@echo -e "$(RED)WARNING: This will destroy all resources!$(NC)"
	@$(TERRAFORM) destroy -auto-approve
	@echo -e "$(GREEN)✓ Resources destroyed$(NC)"

kubectl: ## Configure kubectl for the GKE cluster
	@echo -e "$(YELLOW)Configuring kubectl...$(NC)"
	@if [ -f terraform.tfstate ]; then \
		CLUSTER=$$($(TERRAFORM) output -raw cluster_name 2>/dev/null) && \
		REGION=$$($(TERRAFORM) output -raw region 2>/dev/null) && \
		PROJECT=$$($(TERRAFORM) output -raw project_id 2>/dev/null) && \
		$(GCLOUD) container clusters get-credentials $$CLUSTER --region $$REGION --project $$PROJECT && \
		echo -e "$(GREEN)✓ kubectl configured for cluster: $$CLUSTER$(NC)" && \
		$(KUBECTL) cluster-info; \
	else \
		echo -e "$(RED)✗ No terraform.tfstate found. Run 'make apply' first$(NC)"; \
	fi

clean: ## Clean local Terraform files
	@echo -e "$(YELLOW)Cleaning local Terraform files...$(NC)"
	@rm -rf .terraform terraform.tfstate terraform.tfstate.backup .terraform.lock.hcl
	@echo -e "$(GREEN)✓ Cleaned$(NC)"

install-%: ## Install manifests
	$(MAKE) -C $(MANIFESTS_DIR) install-$*
