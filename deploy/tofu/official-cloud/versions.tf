terraform {
  required_version = ">= 1.11.7, < 1.12.0"
  backend "s3" {}
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  alias  = "us"
  region = var.us.region
  default_tags { tags = local.common_tags }
}

provider "aws" {
  alias  = "eu"
  region = var.eu.region
  default_tags { tags = local.common_tags }
}

locals {
  common_tags = {
    Product     = "Lites"
    Environment = "production"
    ManagedBy   = "OpenTofu"
    Repository  = "langshift/lites"
  }
}
