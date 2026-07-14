output "cell" {
  value = {
    name               = var.name
    residency_region   = var.residency_region
    aws_region         = data.aws_region.current.region
    availability_zones = var.availability_zones
  }
}
output "eks" { value = { name = aws_eks_cluster.this.name, endpoint = aws_eks_cluster.this.endpoint, oidc_issuer = aws_eks_cluster.this.identity[0].oidc[0].issuer } }
output "eventstore" { value = { writer_endpoint = aws_rds_cluster.eventstore.endpoint, reader_endpoint = aws_rds_cluster.eventstore.reader_endpoint, port = aws_rds_cluster.eventstore.port, master_secret_arn = aws_rds_cluster.eventstore.master_user_secret[0].secret_arn } }
output "valkey" { value = { primary_endpoint = aws_elasticache_replication_group.valkey.primary_endpoint_address, reader_endpoint = aws_elasticache_replication_group.valkey.reader_endpoint_address, port = aws_elasticache_replication_group.valkey.port, auth_secret_arn = aws_secretsmanager_secret.valkey_auth.arn } }
output "buckets" { value = { for name, bucket in aws_s3_bucket.data : name => bucket.bucket } }
output "workload_secret_arns" { value = { for name, secret in aws_secretsmanager_secret.workload : name => secret.arn } }
output "external_secrets_role_arn" { value = aws_iam_role.external_secrets.arn }
output "data_kms_key_arn" { value = aws_kms_key.data.arn }
output "recovery" { value = { epoch_table = aws_dynamodb_table.store_epoch.name, backup_vault = aws_backup_vault.this.name, operator_role_arn = aws_iam_role.recovery_operator.arn } }
