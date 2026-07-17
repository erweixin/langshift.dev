locals {
  bucket_names = {
    payloads = "${var.name}-${data.aws_caller_identity.current.account_id}-${data.aws_region.current.region}-payloads"
    artifacts = "${var.name}-${data.aws_caller_identity.current.account_id}-${data.aws_region.current.region}-artifacts"
    imports  = "${var.name}-${data.aws_caller_identity.current.account_id}-${data.aws_region.current.region}-identity-imports"
    audit    = "${var.name}-${data.aws_caller_identity.current.account_id}-${data.aws_region.current.region}-audit"
  }
}

resource "aws_s3_bucket" "data" {
  for_each      = local.bucket_names
  bucket        = each.value
  force_destroy = false
  tags          = merge(local.tags, { "lites.dev/data-class" = each.key })
  lifecycle { prevent_destroy = true }
}

resource "aws_s3_bucket_public_access_block" "data" {
  for_each                = aws_s3_bucket.data
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "data" {
  for_each = aws_s3_bucket.data
  bucket   = each.value.id
  rule { object_ownership = "BucketOwnerEnforced" }
}

resource "aws_s3_bucket_versioning" "data" {
  for_each = aws_s3_bucket.data
  bucket   = each.value.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  for_each = aws_s3_bucket.data
  bucket   = each.value.id
  rule {
    bucket_key_enabled = true
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.data.arn
      sse_algorithm     = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "data" {
  for_each = aws_s3_bucket.data
  bucket   = each.value.id
  rule {
    id     = "bounded-noncurrent-retention"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload { days_after_initiation = 7 }
    noncurrent_version_expiration { noncurrent_days = 35 }
  }
  depends_on = [aws_s3_bucket_versioning.data]
}

resource "aws_s3_bucket_policy" "data" {
  for_each = aws_s3_bucket.data
  bucket   = each.value.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Sid = "DenyInsecureTransport", Effect = "Deny", Principal = "*", Action = "s3:*", Resource = [each.value.arn, "${each.value.arn}/*"], Condition = { Bool = { "aws:SecureTransport" = "false" } } },
      { Sid = "DenyWrongEncryption", Effect = "Deny", Principal = "*", Action = "s3:PutObject", Resource = "${each.value.arn}/*", Condition = { StringNotEquals = { "s3:x-amz-server-side-encryption" = "aws:kms" } } }
    ]
  })
}
