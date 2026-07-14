resource "aws_secretsmanager_secret" "workload" {
  for_each                = local.secret_components
  name                    = "/lites/${var.name}/${each.key}"
  description             = "Runtime file bundle for ${var.name}/${each.key}; values are populated by the audited bootstrap and rotation workflow."
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 30
  tags                    = merge(local.tags, { "lites.dev/component" = each.key })
}

resource "aws_secretsmanager_secret" "valkey_auth" {
  name                    = "/lites/${var.name}/infrastructure/valkey-auth"
  description             = "Rotatable Valkey token used to assemble least-privilege workload bundles."
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 30
  tags                    = local.tags
}

resource "aws_secretsmanager_secret_version" "valkey_auth" {
  secret_id     = aws_secretsmanager_secret.valkey_auth.id
  secret_string = var.valkey_auth_token
}

resource "aws_iam_role" "external_secrets" {
  name               = "${var.name}-external-secrets"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "pods.eks.amazonaws.com" }, Action = ["sts:AssumeRole", "sts:TagSession"] }] })
  tags               = local.tags
}

resource "aws_iam_role_policy" "external_secrets" {
  role = aws_iam_role.external_secrets.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue"], Resource = concat(values(aws_secretsmanager_secret.workload)[*].arn, [aws_secretsmanager_secret.valkey_auth.arn, aws_rds_cluster.eventstore.master_user_secret[0].secret_arn]) },
      { Effect = "Allow", Action = ["kms:Decrypt"], Resource = [aws_kms_key.data.arn], Condition = { StringEquals = { "kms:ViaService" = "secretsmanager.${data.aws_region.current.region}.amazonaws.com" } } }
    ]
  })
}

resource "aws_eks_pod_identity_association" "external_secrets" {
  cluster_name    = aws_eks_cluster.this.name
  namespace       = "external-secrets"
  service_account = "external-secrets"
  role_arn        = aws_iam_role.external_secrets.arn
  depends_on      = [aws_eks_addon.core]
}
