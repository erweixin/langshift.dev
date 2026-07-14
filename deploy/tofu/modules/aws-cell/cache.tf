resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = values(aws_subnet.data)[*].id
  tags       = local.tags
}

resource "aws_security_group" "valkey" {
  name_prefix = "${var.name}-valkey-"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol        = "tcp"
    from_port       = 6379
    to_port         = 6379
    security_groups = [aws_eks_cluster.this.vpc_config[0].cluster_security_group_id]
  }
  tags = local.tags
}

resource "aws_elasticache_replication_group" "valkey" {
  replication_group_id       = "${var.name}-valkey"
  description                = "Lites disposable cache and rate limits for ${var.name}"
  engine                     = "valkey"
  engine_version             = var.valkey_engine_version
  node_type                  = var.valkey_node_type
  port                       = 6379
  num_cache_clusters         = 3
  automatic_failover_enabled = true
  multi_az_enabled           = true
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  auth_token                 = var.valkey_auth_token
  auth_token_update_strategy = "ROTATE"
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.valkey.id]
  snapshot_retention_limit   = 7
  snapshot_window            = "03:00-04:00"
  maintenance_window         = "sun:04:30-sun:05:30"
  auto_minor_version_upgrade = false
  apply_immediately          = false
  tags                       = local.tags
  lifecycle { prevent_destroy = true }
}
