resource "aws_iam_role" "eks_cluster" {
  name               = "${var.name}-eks-cluster"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "eks.amazonaws.com" }, Action = "sts:AssumeRole" }] })
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "eks_cluster" {
  role       = aws_iam_role.eks_cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_eks_cluster" "this" {
  name                      = var.name
  role_arn                  = aws_iam_role.eks_cluster.arn
  version                   = var.kubernetes_version
  enabled_cluster_log_types = ["api", "audit", "authenticator", "controllerManager", "scheduler"]
  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = false
  }
  vpc_config {
    subnet_ids              = values(aws_subnet.application)[*].id
    endpoint_private_access = true
    endpoint_public_access  = false
  }
  encryption_config {
    provider { key_arn = aws_kms_key.data.arn }
    resources = ["secrets"]
  }
  tags       = local.tags
  depends_on = [aws_iam_role_policy_attachment.eks_cluster]
  lifecycle { prevent_destroy = true }
}

resource "aws_eks_access_entry" "administrator" {
  for_each      = toset(var.eks_admin_principal_arns)
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  type          = "STANDARD"
  tags          = local.tags
}

resource "aws_eks_access_policy_association" "administrator" {
  for_each      = aws_eks_access_entry.administrator
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value.principal_arn
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
  access_scope { type = "cluster" }
}

resource "aws_iam_role" "eks_nodes" {
  name               = "${var.name}-eks-nodes"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "ec2.amazonaws.com" }, Action = "sts:AssumeRole" }] })
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "eks_nodes" {
  for_each = toset([
    "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryPullOnly",
    "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
  ])
  role       = aws_iam_role.eks_nodes.name
  policy_arn = each.value
}

resource "aws_eks_node_group" "general" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "general"
  node_role_arn   = aws_iam_role.eks_nodes.arn
  subnet_ids      = values(aws_subnet.application)[*].id
  instance_types  = var.node_instance_types
  capacity_type   = "ON_DEMAND"
  ami_type        = "AL2023_ARM_64_STANDARD"
  disk_size       = 100
  scaling_config {
    min_size     = var.node_min_size
    desired_size = var.node_desired_size
    max_size     = var.node_max_size
  }
  update_config { max_unavailable = 1 }
  labels     = { "lites.dev/pool" = "general" }
  tags       = local.tags
  depends_on = [aws_iam_role_policy_attachment.eks_nodes]
}

resource "aws_eks_addon" "core" {
  for_each                    = { for name, version in var.eks_core_addon_versions : name => version if name != "aws-ebs-csi-driver" }
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = each.key
  addon_version               = each.value
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "PRESERVE"
  tags                        = local.tags
  depends_on                  = [aws_eks_node_group.general]
}

resource "aws_iam_role" "ebs_csi" {
  name               = "${var.name}-ebs-csi"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "pods.eks.amazonaws.com" }, Action = ["sts:AssumeRole", "sts:TagSession"] }] })
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "ebs_csi" {
  role       = aws_iam_role.ebs_csi.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
}

resource "aws_iam_role_policy" "ebs_csi_kms" {
  name = "${var.name}-ebs-csi-kms"
  role = aws_iam_role.ebs_csi.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey"]
        Resource = aws_kms_key.data.arn
      },
      {
        Effect   = "Allow"
        Action   = "kms:CreateGrant"
        Resource = aws_kms_key.data.arn
        Condition = {
          Bool = { "kms:GrantIsForAWSResource" = "true" }
        }
      }
    ]
  })
}

resource "aws_eks_pod_identity_association" "ebs_csi" {
  cluster_name    = aws_eks_cluster.this.name
  namespace       = "kube-system"
  service_account = "ebs-csi-controller-sa"
  role_arn        = aws_iam_role.ebs_csi.arn
  depends_on      = [aws_eks_addon.core, aws_iam_role_policy.ebs_csi_kms]
}

resource "aws_eks_addon" "ebs_csi" {
  cluster_name                = aws_eks_cluster.this.name
  addon_name                  = "aws-ebs-csi-driver"
  addon_version               = var.eks_core_addon_versions["aws-ebs-csi-driver"]
  resolve_conflicts_on_create = "OVERWRITE"
  resolve_conflicts_on_update = "PRESERVE"
  tags                        = local.tags
  depends_on                  = [aws_eks_pod_identity_association.ebs_csi]
}
