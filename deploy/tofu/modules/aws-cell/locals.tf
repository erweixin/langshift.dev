locals {
  az_map = { for index, az in var.availability_zones : az => index }
  tags = merge(var.tags, {
    "lites.dev/cell"       = var.name
    "lites.dev/residency"  = var.residency_region
    "lites.dev/managed-by" = "opentofu"
  })
  secret_components = toset([
    "api-gateway",
    "identity-service",
    "realtime-gateway",
    "behavior-control-plane",
    "identity-import-worker",
    "identity-mail-worker",
    "outbox-publisher",
    "runtime-sweeper",
    "run-cancellation-reconciler",
    "store-epoch-authority",
    "migration",
    "nats-server-tls",
  ])
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}
