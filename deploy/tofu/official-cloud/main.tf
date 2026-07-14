module "us" {
  source    = "../modules/aws-cell"
  providers = { aws = aws.us }

  name                             = var.us.name
  residency_region                 = "US"
  availability_zones               = var.us.availability_zones
  vpc_cidr                         = var.us.vpc_cidr
  public_subnet_cidrs              = var.us.public_subnet_cidrs
  application_subnet_cidrs         = var.us.application_subnet_cidrs
  data_subnet_cidrs                = var.us.data_subnet_cidrs
  kubernetes_version               = var.us.kubernetes_version
  eks_admin_principal_arns         = var.us.eks_admin_principals
  eks_core_addon_versions          = var.us.eks_core_addon_versions
  aurora_engine_version            = var.us.aurora_engine_version
  valkey_engine_version            = var.us.valkey_engine_version
  valkey_auth_token                = var.us_valkey_auth_token
  recovery_operator_principal_arns = var.us.recovery_operator_principals
  tags                             = local.common_tags
}

module "eu" {
  source    = "../modules/aws-cell"
  providers = { aws = aws.eu }

  name                             = var.eu.name
  residency_region                 = "EU"
  availability_zones               = var.eu.availability_zones
  vpc_cidr                         = var.eu.vpc_cidr
  public_subnet_cidrs              = var.eu.public_subnet_cidrs
  application_subnet_cidrs         = var.eu.application_subnet_cidrs
  data_subnet_cidrs                = var.eu.data_subnet_cidrs
  kubernetes_version               = var.eu.kubernetes_version
  eks_admin_principal_arns         = var.eu.eks_admin_principals
  eks_core_addon_versions          = var.eu.eks_core_addon_versions
  aurora_engine_version            = var.eu.aurora_engine_version
  valkey_engine_version            = var.eu.valkey_engine_version
  valkey_auth_token                = var.eu_valkey_auth_token
  recovery_operator_principal_arns = var.eu.recovery_operator_principals
  tags                             = local.common_tags
}

check "cell_residency_isolation" {
  assert {
    condition     = var.us.region != var.eu.region && var.us.vpc_cidr != var.eu.vpc_cidr && var.us.name != var.eu.name
    error_message = "US and EU cells must use different regions, VPC CIDRs and names."
  }
}
