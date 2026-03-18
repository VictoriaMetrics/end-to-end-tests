# Main Terraform configuration for GKE cluster with NGINX Ingress

terraform {
  required_version = ">= 1.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.23"
    }
  }
}

# Configure the Google Cloud Provider
provider "google" {
  project = var.project_id
  region  = var.region
}

# Use an existing VPC by name instead of creating a new one
data "google_compute_network" "vpc" {
  name    = var.vpc_name
  project = var.project_id
}

# Get GKE cluster credentials for Kubernetes/Helm providers
data "google_client_config" "default" {}

# Configure Kubernetes Provider
provider "kubernetes" {
  host                   = "https://${google_container_cluster.primary.endpoint}"
  token                  = data.google_client_config.default.access_token
  cluster_ca_certificate = base64decode(google_container_cluster.primary.master_auth[0].cluster_ca_certificate)
}

# Create GKE cluster
resource "google_container_cluster" "primary" {
  name                     = var.cluster_name
  location                 = var.zone != "" ? var.zone : var.region
  remove_default_node_pool = false
  deletion_protection      = false

  network           = data.google_compute_network.vpc.self_link
  datapath_provider = "ADVANCED_DATAPATH"

  min_master_version = var.kubernetes_version

  cluster_autoscaling {
    enabled             = var.enable_autoscaling
    autoscaling_profile = var.autoscaling_profile
    resource_limits {
      resource_type = "cpu"
      minimum       = (var.min_node_count * 2)
      maximum       = (var.max_node_count * 16)
    }
    resource_limits {
      resource_type = "memory"
      minimum       = (var.min_node_count * 8)
      maximum       = (var.max_node_count * 16)
    }
    auto_provisioning_defaults {
      service_account = google_service_account.kubernetes.email
      management {
        auto_repair  = true
        auto_upgrade = true
      }
      disk_size        = 50
      disk_type        = "pd-standard"
      min_cpu_platform = "Intel Broadwell"
    }
  }

  node_pool {
    name       = "default-nodes"
    node_count = var.min_node_count

    autoscaling {
      min_node_count  = var.min_node_count
      max_node_count  = var.max_node_count
      location_policy = "ANY"
    }

    management {
      auto_repair  = true
      auto_upgrade = true
    }

    node_config {
      preemptible  = false
      machine_type = var.machine_type
      disk_size_gb = var.disk_size_gb
      disk_type    = "pd-standard"
      image_type   = "UBUNTU_CONTAINERD"

      service_account = google_service_account.kubernetes.email

      labels = {
        node_pool = "default-pool"
      }
    }
  }
  node_pool {
    name       = "monitoring-nodes"
    node_count = var.monitoring_min_node_count

    autoscaling {
      min_node_count  = var.monitoring_min_node_count
      max_node_count  = var.monitoring_max_node_count
      location_policy = "ANY"
    }

    management {
      auto_repair  = true
      auto_upgrade = true
    }

    node_config {
      preemptible  = false
      machine_type = var.monitoring_machine_type
      disk_size_gb = var.monitoring_disk_size_gb
      disk_type    = "pd-standard"
      image_type   = "UBUNTU_CONTAINERD"

      service_account = google_service_account.kubernetes.email

      labels = {
        monitoring = "true"
      }

      taint {
        key    = "monitoring"
        value  = "true"
        effect = "NO_SCHEDULE"
      }
    }
  }

  monitoring_config {
    managed_prometheus {
      enabled = false
    }
  }

  logging_config {
    enable_components = []
  }
}

# Ingress firewall rule
resource "google_compute_firewall" "nginx_ingress" {
  name    = "${var.cluster_name}-nginx-ingress"
  network = data.google_compute_network.vpc.name
  project = var.project_id

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }

  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["gke-node", var.cluster_name]
}

# Create a service account for the cluster
resource "google_service_account" "kubernetes" {
  account_id   = "${var.cluster_name}-sa"
  display_name = "Service Account for ${var.cluster_name} GKE cluster"
  project      = var.project_id
}

# Using existing VPC specified by variable `vpc_name`; do not create a new VPC here.
