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
  description = "The GCP zone for the cluster (optional, defaults to region if empty)"
  type        = string
  default     = ""
}

variable "cluster_name" {
  description = "The name of the GKE cluster"
  type        = string
  default     = "vm-testbed-cluster"
}

variable "kubernetes_version" {
  description = "The Kubernetes version for the GKE cluster"
  type        = string
  default     = "1.36"
}

variable "min_node_count" {
  description = "Minimum number of nodes for the default node pool autoscaling"
  type        = number
  default     = 1
}

variable "max_node_count" {
  description = "Maximum number of nodes for the default node pool autoscaling"
  type        = number
  default     = 3
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

variable "enable_autoscaling" {
  description = "Whether to enable autoscaling for the cluster"
  type        = bool
  default     = true
}

variable "autoscaling_profile" {
  description = "The autoscaling profile for the cluster (e.g., BALANCED, OPTIMIZE_UTILIZATION)"
  type        = string
  default     = "OPTIMIZE_UTILIZATION"
}

variable "monitoring_min_node_count" {
  description = "Minimum number of nodes for the monitoring node pool autoscaling"
  type        = number
  default     = 1
}

variable "monitoring_max_node_count" {
  description = "Maximum number of nodes for the monitoring node pool autoscaling"
  type        = number
  default     = 2
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
  description = "The name of the existing GCP VPC network to use for the cluster and firewall"
  type        = string
}
