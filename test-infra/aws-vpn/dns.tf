data "aws_route53_zone" "parent_dns_zone" {
  name = var.parent_domain
}


resource "aws_route53_zone" "child_dns_zone" {
  name    = "${var.child_subdomain}.${data.aws_route53_zone.parent_dns_zone.name}"
  comment = var.child_subdomain_comment

  tags = local.global_tags
}

resource "aws_route53_record" "child_dns_route" {
  zone_id = data.aws_route53_zone.parent_dns_zone.id
  name    = var.child_subdomain
  type    = "NS"
  ttl     = 3600
  records = aws_route53_zone.child_dns_zone.name_servers
}

resource "aws_acm_certificate" "vpn_server" {
  domain_name       = "${local.prefix}gateway.${aws_route53_zone.child_dns_zone.name}"
  validation_method = "DNS"

  tags = local.global_tags

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "vpn_record" {
  for_each = {
    for dvo in aws_acm_certificate.vpn_server.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }

  allow_overwrite = true
  name            = each.value.name
  records         = [each.value.record]
  ttl             = 60
  type            = each.value.type
  zone_id         = aws_route53_zone.child_dns_zone.zone_id
}

resource "aws_acm_certificate_validation" "vpn_server" {
  certificate_arn = aws_acm_certificate.vpn_server.arn

  depends_on = [
    aws_route53_record.vpn_record,
    aws_route53_record.child_dns_route,
  ]

  timeouts {
    create = "10m"
  }
}
