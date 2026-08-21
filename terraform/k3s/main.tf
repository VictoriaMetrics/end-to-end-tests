# Main OpenTofu configuration for a k3s cluster on GCE with NGINX Ingress
#
# k3s (not GKE) is used here because its release channels track every
# upstream Kubernetes minor version (e.g. "v1.28"), including versions GKE
# has already dropped support for. This lets the K8S_VERSION matrix cover
# older releases that a managed GKE control plane can no longer run.

terraform {
  required_version = ">= 1.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 7.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
  }
}

# Configure the Google Cloud Provider
provider "google" {
  project = var.project_id
  region  = var.region
}

locals {
  zone = var.zone != "" ? var.zone : "${var.region}-a"
}

# Use an existing VPC by name instead of creating a new one
data "google_compute_network" "vpc" {
  name    = var.vpc_name
  project = var.project_id
}

data "google_compute_image" "ubuntu" {
  family  = "ubuntu-2404-lts-amd64"
  project = "ubuntu-os-cloud"
}

# Ephemeral SSH keypair used only to bootstrap k3s over SSH from this module
resource "tls_private_key" "ssh" {
  algorithm = "ED25519"
}

resource "local_sensitive_file" "ssh_private_key" {
  content         = tls_private_key.ssh.private_key_openssh
  filename        = "${path.module}/.ssh-${var.cluster_name}"
  file_permission = "0600"
}

# Create a service account for the instances
resource "google_service_account" "kubernetes" {
  account_id   = "${var.cluster_name}-sa"
  display_name = "Service Account for ${var.cluster_name} k3s cluster"
  project      = var.project_id
}

# Firewall: SSH for provisioning, k3s API, and ingress traffic
resource "google_compute_firewall" "k3s" {
  name    = "${var.cluster_name}-k3s"
  network = data.google_compute_network.vpc.name
  project = var.project_id

  allow {
    protocol = "tcp"
    ports    = ["22", "80", "443", "6443"]
  }

  source_ranges = ["0.0.0.0/0"]
  target_tags   = [var.cluster_name]
}

# k3s server (control plane + workload node)
resource "google_compute_instance" "server" {
  name         = "${var.cluster_name}-server"
  zone         = local.zone
  machine_type = var.machine_type
  tags         = [var.cluster_name]

  boot_disk {
    initialize_params {
      image = data.google_compute_image.ubuntu.self_link
      size  = var.disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    network = data.google_compute_network.vpc.self_link
    access_config {}
  }

  service_account {
    email  = google_service_account.kubernetes.email
    scopes = ["cloud-platform"]
  }

  metadata = {
    ssh-keys = "${var.ssh_user}:${tls_private_key.ssh.public_key_openssh}"
  }

  metadata_startup_script = templatefile("${path.module}/templates/k3s-server.sh.tpl", {
    k3s_channel = "v${var.k8s_version}"
    node_label  = "node_pool=default-pool"
  })

  labels = {
    node_pool = "default-pool"
  }

  # Block until k3s has finished installing and written its token/kubeconfig,
  # so dependent resources (agents, token/kubeconfig fetch) never race the
  # server's startup script.
  provisioner "remote-exec" {
    connection {
      type        = "ssh"
      user        = var.ssh_user
      private_key = tls_private_key.ssh.private_key_openssh
      host        = self.network_interface[0].access_config[0].nat_ip
      agent       = false
    }
    inline = [
      "timeout 300 bash -c 'until sudo test -f /var/lib/rancher/k3s/server/node-token && sudo test -f /etc/rancher/k3s/k3s.yaml; do sleep 5; done' || { echo '--- cloud-init status ---'; sudo cloud-init status --long || true; echo '--- startup-script output (last 200 lines) ---'; sudo tail -n 200 /var/log/cloud-init-output.log || true; echo '--- k3s service status ---'; sudo systemctl status k3s --no-pager || true; echo '--- k3s journal (last 100 lines) ---'; sudo journalctl -u k3s -n 100 --no-pager || true; exit 1; }",
    ]
  }
}

# Fetch the join token from the server. remote-exec provisioners cannot
# return output to Terraform, so this shells out to `ssh` directly and reads
# the result back via a local_file data source.
resource "null_resource" "k3s_token" {
  depends_on = [google_compute_instance.server]
  triggers = {
    server_id = google_compute_instance.server.id
  }

  provisioner "local-exec" {
    command = <<-EOT
      set -eu
      ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i '${local_sensitive_file.ssh_private_key.filename}' \
        ${var.ssh_user}@${google_compute_instance.server.network_interface[0].access_config[0].nat_ip} \
        'sudo cat /var/lib/rancher/k3s/server/node-token' \
        > '${path.module}/.node-token-${var.cluster_name}'
    EOT
  }
}

data "local_file" "k3s_token" {
  filename   = "${path.module}/.node-token-${var.cluster_name}"
  depends_on = [null_resource.k3s_token]
}

# Fetch the admin kubeconfig from the server the same way.
resource "null_resource" "k3s_kubeconfig" {
  depends_on = [google_compute_instance.server]
  triggers = {
    server_id = google_compute_instance.server.id
  }

  provisioner "local-exec" {
    command = <<-EOT
      set -eu
      ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -i '${local_sensitive_file.ssh_private_key.filename}' \
        ${var.ssh_user}@${google_compute_instance.server.network_interface[0].access_config[0].nat_ip} \
        'sudo cat /etc/rancher/k3s/k3s.yaml' \
        > '${path.module}/.kubeconfig-${var.cluster_name}'
    EOT
  }
}

data "local_file" "k3s_kubeconfig_raw" {
  filename   = "${path.module}/.kubeconfig-${var.cluster_name}"
  depends_on = [null_resource.k3s_kubeconfig]
}

# Default node pool
resource "google_compute_instance" "agent" {
  count        = var.node_count
  name         = "${var.cluster_name}-agent-${count.index}"
  zone         = local.zone
  machine_type = var.machine_type
  tags         = [var.cluster_name]

  boot_disk {
    initialize_params {
      image = data.google_compute_image.ubuntu.self_link
      size  = var.disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    network = data.google_compute_network.vpc.self_link
    access_config {}
  }

  service_account {
    email  = google_service_account.kubernetes.email
    scopes = ["cloud-platform"]
  }

  metadata = {
    ssh-keys = "${var.ssh_user}:${tls_private_key.ssh.public_key_openssh}"
  }

  metadata_startup_script = templatefile("${path.module}/templates/k3s-agent.sh.tpl", {
    k3s_channel = "v${var.k8s_version}"
    server_ip   = google_compute_instance.server.network_interface[0].network_ip
    node_token  = trimspace(data.local_file.k3s_token.content)
    node_label  = "node_pool=default-pool"
    node_taint  = ""
  })

  labels = {
    node_pool = "default-pool"
  }

  provisioner "remote-exec" {
    connection {
      type        = "ssh"
      user        = var.ssh_user
      private_key = tls_private_key.ssh.private_key_openssh
      host        = self.network_interface[0].access_config[0].nat_ip
      agent       = false
    }
    inline = [
      "timeout 300 bash -c 'until systemctl is-active --quiet k3s-agent; do sleep 5; done' || { echo '--- cloud-init status ---'; sudo cloud-init status --long || true; echo '--- startup-script output (last 200 lines) ---'; sudo tail -n 200 /var/log/cloud-init-output.log || true; echo '--- k3s-agent status ---'; sudo systemctl status k3s-agent --no-pager || true; echo '--- k3s-agent journal (last 100 lines) ---'; sudo journalctl -u k3s-agent -n 100 --no-pager || true; exit 1; }",
    ]
  }
}

# Monitoring node pool (tainted so only monitoring workloads schedule here)
resource "google_compute_instance" "monitoring_agent" {
  count        = var.monitoring_node_count
  name         = "${var.cluster_name}-monitoring-${count.index}"
  zone         = local.zone
  machine_type = var.monitoring_machine_type
  tags         = [var.cluster_name]

  boot_disk {
    initialize_params {
      image = data.google_compute_image.ubuntu.self_link
      size  = var.monitoring_disk_size_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    network = data.google_compute_network.vpc.self_link
    access_config {}
  }

  service_account {
    email  = google_service_account.kubernetes.email
    scopes = ["cloud-platform"]
  }

  metadata = {
    ssh-keys = "${var.ssh_user}:${tls_private_key.ssh.public_key_openssh}"
  }

  metadata_startup_script = templatefile("${path.module}/templates/k3s-agent.sh.tpl", {
    k3s_channel = "v${var.k8s_version}"
    server_ip   = google_compute_instance.server.network_interface[0].network_ip
    node_token  = trimspace(data.local_file.k3s_token.content)
    node_label  = "monitoring=true"
    node_taint  = "monitoring=true:NoSchedule"
  })

  labels = {
    monitoring = "true"
  }

  provisioner "remote-exec" {
    connection {
      type        = "ssh"
      user        = var.ssh_user
      private_key = tls_private_key.ssh.private_key_openssh
      host        = self.network_interface[0].access_config[0].nat_ip
      agent       = false
    }
    inline = [
      "timeout 300 bash -c 'until systemctl is-active --quiet k3s-agent; do sleep 5; done' || { echo '--- cloud-init status ---'; sudo cloud-init status --long || true; echo '--- startup-script output (last 200 lines) ---'; sudo tail -n 200 /var/log/cloud-init-output.log || true; echo '--- k3s-agent status ---'; sudo systemctl status k3s-agent --no-pager || true; echo '--- k3s-agent journal (last 100 lines) ---'; sudo journalctl -u k3s-agent -n 100 --no-pager || true; exit 1; }",
    ]
  }
}
