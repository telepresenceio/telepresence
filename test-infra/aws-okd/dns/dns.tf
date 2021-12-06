variable "parent_domain" {
  type        = string
  description = "The name of the parent domain"
}

variable "child_subdomain" {
  type        = string
  description = "The name of the child domain"
}

variable "child_subdomain_comment" {
  type        = string
  description = "The comment of the child domain"
}

variable "aws_region" {
  type        = string
  description = "The region to create the resources in"
}

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws",
      version = "~> 3.20"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

data "aws_route53_zone" "parent_dns_zone" {
  name = var.parent_domain
}


resource "aws_route53_zone" "child_dns_zone" {
  name    = "${var.child_subdomain}.${data.aws_route53_zone.parent_dns_zone.name}"
  comment = var.child_subdomain_comment
}

resource "aws_route53_record" "child_dns_route" {
  zone_id = data.aws_route53_zone.parent_dns_zone.id
  name    = var.child_subdomain
  type    = "NS"
  ttl     = 3600
  records = aws_route53_zone.child_dns_zone.name_servers
}
