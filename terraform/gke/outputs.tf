# Outputs for GKE cluster

output "cluster_name" {
  description = "The name of the GKE cluster"
  value       = google_container_cluster.primary.name
}

output "cluster_endpoint" {
  description = "The endpoint of the GKE cluster"
  value       = google_container_cluster.primary.endpoint
  sensitive   = true
}

output "cluster_ca_certificate" {
  description = "The CA certificate of the GKE cluster"
  value       = google_container_cluster.primary.master_auth[0].cluster_ca_certificate
  sensitive   = true
}

output "region" {
  description = "The region where the cluster is deployed"
  value       = var.region
}

output "zone" {
  description = "The zone where the cluster is deployed (if zonal cluster)"
  value       = var.zone
}

output "service_account_email" {
  description = "The email of the service account created for the cluster"
  value       = google_service_account.kubernetes.email
}

output "kubectl_config_command" {
  description = "Command to configure kubectl"
  value       = "gcloud container clusters get-credentials ${google_container_cluster.primary.name} --region ${var.region} --project ${var.project_id}"
}

output "project_id" {
  description = "The GCP project ID"
  value       = var.project_id
}
