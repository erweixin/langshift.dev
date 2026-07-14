resource "aws_kms_key" "data" {
  description             = "${var.name} application data"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = false
  tags                    = local.tags
}
resource "aws_kms_alias" "data" {
  name          = "alias/${var.name}-data"
  target_key_id = aws_kms_key.data.key_id
}

resource "aws_kms_key" "recovery" {
  description             = "${var.name} recovery control plane"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  multi_region            = false
  tags                    = local.tags
}
resource "aws_kms_alias" "recovery" {
  name          = "alias/${var.name}-recovery"
  target_key_id = aws_kms_key.recovery.key_id
}
