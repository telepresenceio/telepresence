data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  global_tags = {
    "environment" = "${var.child_subdomain}-eks-vpn"
  }
  availability_zones = [for x in ["a", "b", "c"] : "${var.aws_region}${x}"]
  prefix             = "tp-test-vpn-"
}

variable "parent_domain" {
  type        = string
  description = "An already existing DNS zone to create child_dns_zone in"
}

variable "child_subdomain" {
  type        = string
  description = "The prefix to a DNS zone to create. e.g. if parent_domain is 'foo.com', this should be 'bar', to create a hosted zone 'foo.bar.com'"
}

variable "child_subdomain_comment" {
  type        = string
  description = "The description of the created DNS zone; will be visible in the AWS console"
}

variable "aws_region" {
  type        = string
  description = "The AWS region to provision resources in"
}

variable "vpc_cidr" {
  type        = string
  description = "The CIDR for the VPN"
}

variable "vpn_client_cidr" {
  type        = string
  description = "The CIDR assigned to clients of the VPN"
}

variable "split_tunnel" {
  type        = bool
  description = "Whether to set up split tunneling for the VPN"
}

variable "service_cidr" {
  type        = string
  description = "The CIDR to put services in"
}
