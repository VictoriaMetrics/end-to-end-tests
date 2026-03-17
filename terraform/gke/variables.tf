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
  default     = "1.34"
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

variable "subnet_cidr" {
  description = "CIDR range for the primary subnet"
  type        = string
  default     = "10.0.0.0/20"
}

variable "pods_cidr" {
  description = "CIDR range for GKE pods"
  type        = string
  default     = "10.1.0.0/16"
}

variable "services_cidr" {
  description = "CIDR range for GKE services"
  type        = string
  default     = "10.2.0.0/16"
}

variable "vm_min_node_count" {
  description = "Minimum number of nodes for the VictoriaMetrics node pool autoscaling"
  type        = number
  default     = 1
}

variable "vm_max_node_count" {
  description = "Maximum number of nodes for the VictoriaMetrics node pool autoscaling"
  type        = number
  default     = 3
}

variable "vm_machine_type" {
  description = "Machine type for VictoriaMetrics nodes"
  type        = string
  default     = "e2-medium"
}

variable "vm_disk_size_gb" {
  description = "Disk size for VM nodes"
  type        = number
  default     = 50
}

variable "vm_preemptible_nodes" {
  description = "Whether to use preemptible VMs for VictoriaMetrics nodes"
  type        = bool
  default     = true
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

variable "update_domain" {
  description = "Whether to update the DNS record for the k8s.cloud.vrutkovs.eu domain"
  type        = bool
  default     = false
}

variable "domain" {
  description = "Base domain name for DNS records"
  type        = string
  default     = "vrutkovs.eu"
}

variable "gcp_dns_zone_name" {
  description = "The name of the GCP DNS managed zone"
  type        = string
  default     = "cloud"
}

variable "ingress_external_ip" {
  description = "External IP address of the ingress service"
  type        = string
  default     = ""
}

variable "vpc_name" {
  description = "The name of the existing GCP VPC network to use for the cluster and firewall"
  type        = string
}
