# Outputs for the k3s cluster

output "cluster_name" {
  description = "The name of the k3s cluster"
  value       = var.cluster_name
}

output "server_ip" {
  description = "The external IP of the k3s server"
  value       = google_compute_instance.server.network_interface[0].access_config[0].nat_ip
}

output "kubeconfig" {
  description = "Admin kubeconfig for the cluster, with the server address rewritten to the server's external IP"
  value = replace(
    data.local_file.k3s_kubeconfig_raw.content,
    "127.0.0.1",
    google_compute_instance.server.network_interface[0].access_config[0].nat_ip
  )
  sensitive = true
}

output "region" {
  description = "The region where the cluster is deployed"
  value       = var.region
}

output "zone" {
  description = "The zone where the instances are deployed"
  value       = local.zone
}

output "service_account_email" {
  description = "The email of the service account created for the cluster"
  value       = google_service_account.kubernetes.email
}

output "ssh_command" {
  description = "Command to SSH into the k3s server"
  value       = "ssh -i ${local_sensitive_file.ssh_private_key.filename} ${var.ssh_user}@${google_compute_instance.server.network_interface[0].access_config[0].nat_ip}"
}

output "project_id" {
  description = "The GCP project ID"
  value       = var.project_id
}
