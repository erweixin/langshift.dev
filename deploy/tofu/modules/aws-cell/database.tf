resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = values(aws_subnet.data)[*].id
  tags       = local.tags
}

resource "aws_security_group" "database" {
  name_prefix = "${var.name}-database-"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol        = "tcp"
    from_port       = 5432
    to_port         = 5432
    security_groups = [aws_eks_cluster.this.vpc_config[0].cluster_security_group_id]
  }
  tags = local.tags
}

resource "aws_rds_cluster_parameter_group" "this" {
  name   = "${var.name}-aurora-postgresql16"
  family = "aurora-postgresql16"
  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }
  parameter {
    name         = "log_connections"
    value        = "1"
    apply_method = "immediate"
  }
  parameter {
    name         = "log_disconnections"
    value        = "1"
    apply_method = "immediate"
  }
  tags = local.tags
}

resource "aws_rds_cluster" "eventstore" {
  cluster_identifier                  = "${var.name}-eventstore"
  engine                              = "aurora-postgresql"
  engine_version                      = var.aurora_engine_version
  engine_mode                         = "provisioned"
  database_name                       = "lites"
  master_username                     = "lites_admin"
  manage_master_user_password         = true
  master_user_secret_kms_key_id       = aws_kms_key.data.arn
  availability_zones                  = var.availability_zones
  db_subnet_group_name                = aws_db_subnet_group.this.name
  vpc_security_group_ids              = [aws_security_group.database.id]
  db_cluster_parameter_group_name     = aws_rds_cluster_parameter_group.this.name
  storage_encrypted                   = true
  kms_key_id                          = aws_kms_key.data.arn
  backup_retention_period             = 35
  preferred_backup_window             = "02:00-03:00"
  preferred_maintenance_window        = "sun:03:30-sun:04:30"
  enabled_cloudwatch_logs_exports     = ["postgresql"]
  iam_database_authentication_enabled = true
  copy_tags_to_snapshot               = true
  deletion_protection                 = true
  skip_final_snapshot                 = false
  final_snapshot_identifier           = "${var.name}-eventstore-final"
  apply_immediately                   = false
  tags                                = local.tags
  lifecycle { prevent_destroy = true }
}

resource "aws_rds_cluster_instance" "eventstore" {
  count                           = 3
  identifier                      = "${var.name}-eventstore-${count.index + 1}"
  cluster_identifier              = aws_rds_cluster.eventstore.id
  instance_class                  = var.aurora_instance_class
  engine                          = aws_rds_cluster.eventstore.engine
  engine_version                  = aws_rds_cluster.eventstore.engine_version
  availability_zone               = var.availability_zones[count.index]
  db_subnet_group_name            = aws_db_subnet_group.this.name
  auto_minor_version_upgrade      = false
  performance_insights_enabled    = true
  performance_insights_kms_key_id = aws_kms_key.data.arn
  publicly_accessible             = false
  copy_tags_to_snapshot           = true
  tags                            = local.tags
  lifecycle { prevent_destroy = true }
}
