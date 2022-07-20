# Setting up an openshift environment to test

The resources in this folder should help you set up an openshift environment on AWS.
You can use this to test the compatibility of Telepresence within openshift.

## 0. Prerequisites

* A route53 zone in your AWS account. A hosted zone will be created as a subdomain of this existing zone to serve as the DNS name for the VPN's certificates.
* A configured, logged-in AWS CLI
* `terraform` must be installed, then you'll need to run `terraform init` in the `dns` directory
* An account on [RedHat's portal](https://console.redhat.com/)

## 1. Setting up DNS

A DNS hosted zone needs to be created for the cluster to be accessible.
It is suggested that you create this as a subdomain of an already existing zone for a domain that you own.

To do this, simply cd into the `dns` directory, and create a `terraform.tfvars` file like the following:

```hcl
parent_domain           = "foo.net" # The name of an existing route 53 hosted zone
child_subdomain         = "child" # The name of the subdomain -- a zone "child.foo.net" will be created.
child_subdomain_comment = "My DNS zone for openshift" # A human readable comment for the hosted zone
aws_region              = "us-west-2" # The AWS region to create the hosted zone in
```

## 2. Create an ssh keypair for openshift

You'll need an ssh private/public key pair to login to your openshift nodes.
To do this, simply:

```bash
ssh-keygen -t ed25519 -N '' -f ~/.ssh/openshift
```

Then, set up an ssh agent and add the key to it:

```bash
eval `ssh-agent -s`
ssh-add ~/.ssh/openshift
```

## 3. Download openshift installer

Download an openshift installer from [this page](https://github.com/openshift/okd/releases).
Its name will look like `openshift-install-mac-4.8.0-0.okd-2021-11-14-052418.tar.gz` (with differences for version and OS).
Extract the installer somewhere on your computer.

## 4. Run the Openshift installer

At this point all that's left to do is to launch the installer:

```bash
./openshift-install create cluster --dir=./tele-test --log-level=info
```

This installer will ask you a number of questions, starting with asking you to select an SSH key.
Simply select the one you created in step 2:

```
? SSH Public Key  [Use arrows to move, type to filter, ? for more help]
  /Users/USERNAME/.ssh/id_rsa.pub
> /Users/USERNAME/.ssh/openshift.pub
  <none>
```

You'll then have to select `aws` as the platform:

```
? Platform  [Use arrows to move, type to filter, ? for more help]
> aws
  azure
  gcp
  openstack
  ovirt
  vsphere
```

Then select the AWS region from step 1:

```
? Region  [Use arrows to move, type to filter, ? for more help]
  eu-west-3 (Europe (Paris))
  me-south-1 (Middle East (Bahrain))
  sa-east-1 (South America (Sao Paulo))
  us-east-1 (US East (N. Virginia))
  us-east-2 (US East (Ohio))
  us-west-1 (US West (N. California))
> us-west-2 (US West (Oregon))
```

The installer will next ask you for a domain -- find the domain from step 1:

```
? Base Domain  [Use arrows to move, type to filter, ? for more help]
  bar.org
  abc.foo.net
  xyz.foo.net
> child.foo.net
  foo.net
  bar.foo.net
  etc.foo.net
```

Then the name of the cluster:

```
? Cluster Name [? for help] my-test-okd
```

And finally a pull secret; to get this pull secret, login to [https://console.redhat.com/openshift/install/pull-secret](https://console.redhat.com/openshift/install/pull-secret):

```
? Pull Secret **************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************************
```

At that point, the cluster will be created.
This may take slightly longer than an hour. At the end, the installer will prompt you to update your kubeconfig:

```
INFO Install complete!
INFO To access the cluster as the system:admin user when using 'oc', run 'export KUBECONFIG=/Users/USER/openshift/tele-test/auth/kubeconfig'
INFO Access the OpenShift web-console here: https://console-openshift-console.apps.my-test-okd.child.foo.net
INFO Login to the console with user: "kubeadmin", and password: "XXXXX-XXXXX-XXXXX-XXXXX"
```

Once you've `export`ed your kubeconfig, you'll have a usable openshift cluster!

## 5. Install Telepresence

Installing Telepresence on openshift requires some special configuration.

The easiest way to do this is to install through the Helm chart, from
the root of your telepresence.git checkout (`../../` from this
directory), run:

```bash
mkdir tmpdir
go run ./packaging/gen_chart.go tmpdir
helm install traffic-manager ./tmpdir/telepresence-*.tgz -n ambassador --create-namespace --set securityContext=null
```

At that point, `telepresence connect` should work, and you can start doing testing!

## 6. Destroy the cluster

You probably don't want the cluster to hang around forever if you're just using it to test Telepresence.
To destroy it, simply run:

```bash
./openshift-install destroy cluster --dir=./tele-test --log-level=info
```
