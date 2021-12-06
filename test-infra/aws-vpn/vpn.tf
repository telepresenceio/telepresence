resource "aws_acm_certificate" "vpn_client_root" {
  private_key       = file("certs/VPNCert.key")
  certificate_body  = file("certs/VPNCert.crt")
  certificate_chain = file("certs/ca-chain.crt")

  tags = local.global_tags
}

resource "aws_security_group" "vpn_access" {
  vpc_id = aws_vpc.main.id
  name   = "${var.child_subdomain}-${local.prefix}-vpn-sg"

  ingress {
    from_port   = 443
    protocol    = "UDP"
    to_port     = 443
    cidr_blocks = ["0.0.0.0/0"]
    description = "Incoming VPN connection"
  }

  egress {
    from_port   = 0
    protocol    = "-1"
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.global_tags
}

resource "aws_ec2_client_vpn_endpoint" "vpn" {
  description            = "VPN endpoint for ${local.prefix}.${var.child_subdomain}.${var.parent_domain}"
  client_cidr_block      = var.vpn_client_cidr
  split_tunnel           = var.split_tunnel
  server_certificate_arn = aws_acm_certificate_validation.vpn_server.certificate_arn
  dns_servers            = [cidrhost(var.vpc_cidr, 2)]

  authentication_options {
    type                       = "certificate-authentication"
    root_certificate_chain_arn = aws_acm_certificate.vpn_client_root.arn
  }

  connection_log_options {
    enabled = false
  }

  tags = local.global_tags
}

output "vpn_id" {
  value = aws_ec2_client_vpn_endpoint.vpn.id
}

resource "aws_ec2_client_vpn_route" "internet_access" {
  count                  = var.split_tunnel ? 0 : length(aws_subnet.sn_az)
  client_vpn_endpoint_id = aws_ec2_client_vpn_endpoint.vpn.id
  destination_cidr_block = "0.0.0.0/0"
  # These are routed to the internet anyway via aws_route_table.rt so this will ensure that outbound traffic
  # manages to leave.
  target_vpc_subnet_id = aws_subnet.sn_az[count.index].id
}

resource "aws_ec2_client_vpn_network_association" "vpn_subnets" {
  count = length(aws_subnet.sn_az)

  client_vpn_endpoint_id = aws_ec2_client_vpn_endpoint.vpn.id
  subnet_id              = aws_subnet.sn_az[count.index].id
  security_groups        = [aws_security_group.vpn_access.id]

  lifecycle {
    // The issue why we are ignoring changes is that on every change
    // terraform screws up most of the vpn assosciations
    // see: https://github.com/hashicorp/terraform-provider-aws/issues/14717
    ignore_changes = [subnet_id]
  }
}

resource "aws_ec2_client_vpn_authorization_rule" "vpn_auth_rule" {
  client_vpn_endpoint_id = aws_ec2_client_vpn_endpoint.vpn.id
  target_network_cidr    = "0.0.0.0/0"
  authorize_all_groups   = true
}
