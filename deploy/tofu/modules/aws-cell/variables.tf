variable "name" {
  type        = string
  description = "Globally recognizable cell name, for example lites-us-1."
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,23}$", var.name))
    error_message = "name must be a 3-24 character lowercase DNS label."
  }
}

variable "residency_region" {
  type        = string
  description = "Contractual residency boundary exposed to the application (US or EU)."
  validation {
    condition     = contains(["US", "EU"], var.residency_region)
    error_message = "residency_region must be US or EU."
  }
}

variable "availability_zones" {
  type = list(string)
  validation {
    condition     = length(var.availability_zones) == 3 && length(distinct(var.availability_zones)) == 3
    error_message = "exactly three distinct availability zones are required."
  }
}

variable "vpc_cidr" { type = string }
variable "public_subnet_cidrs" {
  type = list(string)
  validation {
    condition     = length(var.public_subnet_cidrs) == 3
    error_message = "three public subnet CIDRs are required."
  }
}
variable "application_subnet_cidrs" {
  type = list(string)
  validation {
    condition     = length(var.application_subnet_cidrs) == 3
    error_message = "three application subnet CIDRs are required."
  }
}
variable "data_subnet_cidrs" {
  type = list(string)
  validation {
    condition     = length(var.data_subnet_cidrs) == 3
    error_message = "three data subnet CIDRs are required."
  }
}

variable "kubernetes_version" { type = string }
variable "eks_admin_principal_arns" {
  type        = list(string)
  description = "IAM principals granted cluster-scoped administrative access through the EKS access API."
  validation {
    condition     = length(var.eks_admin_principal_arns) > 0 && alltrue([for arn in var.eks_admin_principal_arns : startswith(arn, "arn:aws:iam::")])
    error_message = "at least one IAM EKS administrator principal ARN is required."
  }
}
variable "eks_core_addon_versions" {
  type = map(string)
  validation {
    condition     = alltrue([for name in ["vpc-cni", "coredns", "kube-proxy", "eks-pod-identity-agent", "aws-ebs-csi-driver"] : contains(keys(var.eks_core_addon_versions), name) && var.eks_core_addon_versions[name] != ""])
    error_message = "all five EKS add-ons must have explicit versions."
  }
}
variable "node_instance_types" {
  type    = list(string)
  default = ["m7g.xlarge"]
}
variable "node_min_size" {
  type    = number
  default = 3
}
variable "node_desired_size" {
  type    = number
  default = 6
}
variable "node_max_size" {
  type    = number
  default = 30
}

variable "aurora_engine_version" { type = string }
variable "aurora_instance_class" {
  type    = string
  default = "db.r7g.large"
}
variable "valkey_engine_version" { type = string }
variable "valkey_node_type" {
  type    = string
  default = "cache.r7g.large"
}
variable "valkey_auth_token" {
  type        = string
  sensitive   = true
  description = "Rotatable Valkey AUTH token; supply through the encrypted CI secret channel, never tfvars in Git."
  validation {
    condition     = length(var.valkey_auth_token) >= 32 && length(var.valkey_auth_token) <= 128
    error_message = "valkey_auth_token must contain 32-128 characters."
  }
}

variable "recovery_operator_principal_arns" {
  type = list(string)
  validation {
    condition     = length(var.recovery_operator_principal_arns) > 0 && alltrue([for arn in var.recovery_operator_principal_arns : startswith(arn, "arn:aws:iam::")])
    error_message = "at least one IAM recovery operator principal ARN is required."
  }
}

variable "tags" {
  type    = map(string)
  default = {}
}
