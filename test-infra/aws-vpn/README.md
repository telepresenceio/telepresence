# aws-vpn infrastructure example

This example will provision a VPC on AWS, together with an EKS cluster that's private to the VPC and a ClientVPN endpoint to access it.
This is basically all that is needed for a private EKS cluster inside a VPC, and can be used to test how telepresence interacts with different VPN scenarios.

## How to use it

### 0. Prerequisites

You will need a route53 zone in your AWS account.
A hosted zone will be created as a subdomain of this existing zone to serve as the DNS name for the VPN's certificates.

You'll also need to configure your `aws` CLI and authenticate into AWS. Please read the [AWS docs](https://docs.aws.amazon.com/cli/latest/userguide/cli-chap-welcome.html) to know how to do this.

Finally, you need to install [terraform](https://www.terraform.io/) and run `terraform init` in the `aws-vpn` directory (this directory!)

### 1. Generating PKI

First, you need to generate key material for the VPN.
This can be done by simply running the `pki.sh` script in the `aws-vpn` directory.
The certs and keys for the VPN will be placed in a `certs` folder

### 2. Configuration

Next, you need to configure this Terraform stack to generate a VPC/VPN/Cluster with the parameters you need.
The easiest way to do this is to create a `terraform.tfvars` file inside the `aws-vpn` directory and place the configuration's variables there.
The format of this file should be:


```hcl
aws_region              = "us-east-1" # The AWS region to use
parent_domain           = "foo.net" # The hosted zone mentioned in section 0
child_subdomain         = "my-subdomain" # The name of the subdomain that will be created under it.
child_subdomain_comment = "My subdomain's comment" # A human-readable description for the subdomain
vpc_cidr                = "10.0.0.0/16" # The CIDR range for IP addresses within the VPC
vpn_client_cidr         = "10.20.0.0/22" # The CIDR range for clients that connect to the VPN
service_cidr            = "10.19.0.0/16" # The CIDR range for k8s services in the EKS cluster
split_tunnel            = true # Whether the VPN should be configured with split tunneling
```

### 3. Deploying


Now all you have to do is apply the configuration:

```bash
terraform apply
```

Terraform will show you the infrasturcture to provision and ask for confirmation.

### 4. Connecting

First, you will have to download the VPN configuration from AWS. The following command will download it and place it in a `config.ovpn` file

```bash
# Note that you may need to pass a --region flag
aws ec2 export-client-vpn-client-configuration --client-vpn-endpoint-id $(terraform output -raw vpn_id) | jq -r .ClientConfiguration > config.ovpn
```

Note that it does not include the client cert and key, to add those simply:

```bash
echo "<cert>" >> config.ovpn
cat certs/VPNCert.crt >> config.ovpn
echo "</cert>" >> config.ovpn
echo "<key>" >> config.ovpn
cat certs/VPNCert.key >> config.ovpn
echo "</key>" >> config.ovpn
```

At that point, you should be able to import the `config.ovpn` file into any OpenVPN client.

Lastly, you'll need to download the kubernetes configuration:

```bash
aws eks --region us-east-1 update-kubeconfig --name $(terraform output -raw eks_name)
```

Once you do this, and connect to the VPN through your client, `kubectl` should be connected to the new cluster!
