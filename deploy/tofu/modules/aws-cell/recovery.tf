resource "aws_dynamodb_table" "store_epoch" {
  name                        = "${var.name}-store-epoch"
  billing_mode                = "PAY_PER_REQUEST"
  hash_key                    = "cell_id"
  deletion_protection_enabled = true
  attribute {
    name = "cell_id"
    type = "S"
  }
  point_in_time_recovery { enabled = true }
  server_side_encryption {
    enabled     = true
    kms_key_arn = aws_kms_key.recovery.arn
  }
  tags = merge(local.tags, { "lites.dev/recovery-control-plane" = "true" })
  lifecycle { prevent_destroy = true }
}

resource "aws_backup_vault" "this" {
  name        = "${var.name}-recovery"
  kms_key_arn = aws_kms_key.recovery.arn
  tags        = local.tags
  lifecycle { prevent_destroy = true }
}

resource "aws_backup_vault_lock_configuration" "this" {
  backup_vault_name   = aws_backup_vault.this.name
  changeable_for_days = 7
  min_retention_days  = 35
  max_retention_days  = 3650
}

resource "aws_backup_plan" "this" {
  name = "${var.name}-continuous"
  rule {
    rule_name                = "continuous-and-daily"
    target_vault_name        = aws_backup_vault.this.name
    schedule                 = "cron(0 5 * * ? *)"
    start_window             = 60
    completion_window        = 360
    enable_continuous_backup = true
    lifecycle {
      cold_storage_after = 30
      delete_after       = 365
    }
    recovery_point_tags = local.tags
  }
  tags = local.tags
}

resource "aws_iam_role" "backup" {
  name               = "${var.name}-backup"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "backup.amazonaws.com" }, Action = "sts:AssumeRole" }] })
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "backup" {
  for_each = toset([
    "arn:aws:iam::aws:policy/service-role/AWSBackupServiceRolePolicyForBackup",
    "arn:aws:iam::aws:policy/service-role/AWSBackupServiceRolePolicyForRestores",
  ])
  role       = aws_iam_role.backup.name
  policy_arn = each.value
}

resource "aws_backup_selection" "this" {
  name         = "${var.name}-durable-data"
  plan_id      = aws_backup_plan.this.id
  iam_role_arn = aws_iam_role.backup.arn
  resources    = concat([aws_rds_cluster.eventstore.arn], values(aws_s3_bucket.data)[*].arn)
  depends_on   = [aws_iam_role_policy_attachment.backup]
}

resource "aws_iam_role" "recovery_operator" {
  name               = "${var.name}-recovery-operator"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { AWS = var.recovery_operator_principal_arns }, Action = "sts:AssumeRole", Condition = { Bool = { "aws:MultiFactorAuthPresent" = "true" } } }] })
  tags               = local.tags
}

resource "aws_iam_role_policy" "recovery_operator" {
  role = aws_iam_role.recovery_operator.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "EpochCAS", Effect = "Allow", Action = ["dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:TransactWriteItems"], Resource = aws_dynamodb_table.store_epoch.arn, Condition = { "ForAllValues:StringEquals" = { "dynamodb:LeadingKeys" = [var.name] } } },
      { Sid = "PublishEpochRuntimeBundle", Effect = "Allow", Action = ["secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue", "secretsmanager:PutSecretValue"], Resource = aws_secretsmanager_secret.workload["store-epoch-authority"].arn },
      { Sid = "DecryptRecoveryControl", Effect = "Allow", Action = ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey"], Resource = aws_kms_key.recovery.arn }
    ]
  })
}
