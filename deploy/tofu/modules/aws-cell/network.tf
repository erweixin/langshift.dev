resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = merge(local.tags, { Name = var.name })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.tags, { Name = "${var.name}-igw" })
}

resource "aws_subnet" "public" {
  for_each                = local.az_map
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = var.public_subnet_cidrs[each.value]
  map_public_ip_on_launch = false
  tags = merge(local.tags, {
    Name                     = "${var.name}-public-${each.key}"
    "kubernetes.io/role/elb" = "1"
  })
}

resource "aws_subnet" "application" {
  for_each                = local.az_map
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = var.application_subnet_cidrs[each.value]
  map_public_ip_on_launch = false
  tags = merge(local.tags, {
    Name                              = "${var.name}-application-${each.key}"
    "kubernetes.io/role/internal-elb" = "1"
  })
}

resource "aws_subnet" "data" {
  for_each                = local.az_map
  vpc_id                  = aws_vpc.this.id
  availability_zone       = each.key
  cidr_block              = var.data_subnet_cidrs[each.value]
  map_public_ip_on_launch = false
  tags                    = merge(local.tags, { Name = "${var.name}-data-${each.key}" })
}

resource "aws_eip" "nat" {
  for_each   = local.az_map
  domain     = "vpc"
  tags       = merge(local.tags, { Name = "${var.name}-nat-${each.key}" })
  depends_on = [aws_internet_gateway.this]
}

resource "aws_nat_gateway" "this" {
  for_each      = local.az_map
  allocation_id = aws_eip.nat[each.key].id
  subnet_id     = aws_subnet.public[each.key].id
  tags          = merge(local.tags, { Name = "${var.name}-nat-${each.key}" })
  depends_on    = [aws_internet_gateway.this]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = merge(local.tags, { Name = "${var.name}-public" })
}

resource "aws_route_table_association" "public" {
  for_each       = local.az_map
  subnet_id      = aws_subnet.public[each.key].id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table" "application" {
  for_each = local.az_map
  vpc_id   = aws_vpc.this.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this[each.key].id
  }
  tags = merge(local.tags, { Name = "${var.name}-application-${each.key}" })
}

resource "aws_route_table_association" "application" {
  for_each       = local.az_map
  subnet_id      = aws_subnet.application[each.key].id
  route_table_id = aws_route_table.application[each.key].id
}

resource "aws_route_table" "data" {
  for_each = local.az_map
  vpc_id   = aws_vpc.this.id
  tags     = merge(local.tags, { Name = "${var.name}-data-${each.key}" })
}

resource "aws_route_table_association" "data" {
  for_each       = local.az_map
  subnet_id      = aws_subnet.data[each.key].id
  route_table_id = aws_route_table.data[each.key].id
}

resource "aws_security_group" "endpoints" {
  name_prefix = "${var.name}-endpoints-"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol    = "tcp"
    from_port   = 443
    to_port     = 443
    cidr_blocks = [var.vpc_cidr]
  }
  tags = local.tags
}

resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${data.aws_region.current.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = concat(values(aws_route_table.application)[*].id, values(aws_route_table.data)[*].id)
  tags              = merge(local.tags, { Name = "${var.name}-s3" })
}

resource "aws_vpc_endpoint" "interface" {
  for_each            = toset(["ecr.api", "ecr.dkr", "kms", "logs", "secretsmanager", "sts"])
  vpc_id              = aws_vpc.this.id
  service_name        = "com.amazonaws.${data.aws_region.current.region}.${each.key}"
  vpc_endpoint_type   = "Interface"
  private_dns_enabled = true
  subnet_ids          = values(aws_subnet.application)[*].id
  security_group_ids  = [aws_security_group.endpoints.id]
  tags                = merge(local.tags, { Name = "${var.name}-${replace(each.key, ".", "-")}" })
}

resource "aws_cloudwatch_log_group" "flow" {
  name              = "/lites/${var.name}/vpc-flow"
  retention_in_days = 365
  tags              = local.tags
}

resource "aws_iam_role" "flow" {
  name               = "${var.name}-vpc-flow"
  assume_role_policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Principal = { Service = "vpc-flow-logs.amazonaws.com" }, Action = "sts:AssumeRole" }] })
  tags               = local.tags
}

resource "aws_iam_role_policy" "flow" {
  role   = aws_iam_role.flow.id
  policy = jsonencode({ Version = "2012-10-17", Statement = [{ Effect = "Allow", Action = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogGroups", "logs:DescribeLogStreams"], Resource = "${aws_cloudwatch_log_group.flow.arn}:*" }] })
}

resource "aws_flow_log" "this" {
  vpc_id                   = aws_vpc.this.id
  traffic_type             = "ALL"
  log_destination_type     = "cloud-watch-logs"
  log_destination          = aws_cloudwatch_log_group.flow.arn
  iam_role_arn             = aws_iam_role.flow.arn
  max_aggregation_interval = 60
  tags                     = local.tags
}
