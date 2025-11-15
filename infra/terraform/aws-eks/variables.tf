variable "region" {
  type        = string
  description = "AWS region for sandbox cluster"
  default     = "us-east-1"
}

variable "cluster_name" {
  type        = string
  description = "Name of the EKS cluster"
  default     = "trackshift-sandbox"
}

variable "vpc_id" {
  type        = string
  description = "VPC hosting the cluster"
}

variable "private_subnets" {
  type        = list(string)
  description = "Private subnet IDs for nodes"
}