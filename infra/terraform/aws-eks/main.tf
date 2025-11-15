terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

module "eks" {
  source          = "terraform-aws-modules/eks/aws"
  version         = "~> 20.0"
  cluster_name    = var.cluster_name
  cluster_version = "1.29"
  subnets         = var.private_subnets
  vpc_id          = var.vpc_id

  eks_managed_node_groups = {
    default = {
      min_size     = 2
      max_size     = 6
      desired_size = 3
      instance_types = ["m6i.large"]
    }
  }
}

module "observability" {
  source = "git::https://github.com/cloudposse/terraform-aws-eks-monitoring.git"
  cluster_identity_oidc_issuer     = module.eks.cluster_oidc_issuer_url
  cluster_identity_oidc_issuer_arn = module.eks.oidc_provider_arn
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}