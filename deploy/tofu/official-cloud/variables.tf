variable "us" {
  type = object({
    region                       = string
    name                         = string
    availability_zones           = list(string)
    vpc_cidr                     = string
    public_subnet_cidrs          = list(string)
    application_subnet_cidrs     = list(string)
    data_subnet_cidrs            = list(string)
    kubernetes_version           = string
    eks_admin_principals         = list(string)
    eks_core_addon_versions      = map(string)
    aurora_engine_version        = string
    valkey_engine_version        = string
    recovery_operator_principals = list(string)
  })
}

variable "eu" {
  type = object({
    region                       = string
    name                         = string
    availability_zones           = list(string)
    vpc_cidr                     = string
    public_subnet_cidrs          = list(string)
    application_subnet_cidrs     = list(string)
    data_subnet_cidrs            = list(string)
    kubernetes_version           = string
    eks_admin_principals         = list(string)
    eks_core_addon_versions      = map(string)
    aurora_engine_version        = string
    valkey_engine_version        = string
    recovery_operator_principals = list(string)
  })
}

variable "us_valkey_auth_token" {
  type      = string
  sensitive = true
}
variable "eu_valkey_auth_token" {
  type      = string
  sensitive = true
}
