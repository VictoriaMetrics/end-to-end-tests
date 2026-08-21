variable "project_id" {
  description = "The GCP project ID"
  type        = string
}

variable "region" {
  description = "The GCP region to deploy resources"
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "The GCP zone for the instances (optional, defaults to region + \"-a\")"
  type        = string
  default     = ""
}

variable "cluster_name" {
  description = "The name of the k3s cluster (used to prefix instance names)"
  type        = string
  default     = "vm-testbed-cluster"
}

variable "k8s_version" {
  description = "Kubernetes minor version (e.g. \"1.28\"); resolved via the matching k3s release channel"
  type        = string
  default     = "1.36"
}

variable "ssh_user" {
  description = "Username used to SSH into the instances and provision k3s"
  type        = string
  default     = "ubuntu"
}

variable "node_count" {
  description = "Number of k3s agent nodes in the default node pool"
  type        = number
  default     = 2
}

variable "machine_type" {
  description = "Machine type for the default node pool"
  type        = string
  default     = "e2-medium"
}

variable "disk_size_gb" {
  description = "Disk size for the default node pool in GB"
  type        = number
  default     = 50
}

variable "monitoring_node_count" {
  description = "Number of k3s agent nodes in the monitoring node pool"
  type        = number
  default     = 1
}

variable "monitoring_machine_type" {
  description = "Machine type for monitoring nodes"
  type        = string
  default     = "e2-standard-4"
}

variable "monitoring_disk_size_gb" {
  description = "Disk size for monitoring nodes in GB"
  type        = number
  default     = 50
}

variable "vpc_name" {
  description = "The name of the existing GCP VPC network to use for the instances and firewall"
  type        = string
}
