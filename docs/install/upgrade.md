---
title: Upgrade client
description: "How to upgrade your installation of Telepresence and install previous versions."
hide_table_of_contents: true
---

import Platform from '@site/src/components/Platform';

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.

Before upgrading your CLI, you must stop any live Telepresence processes by issuing `telepresence quit -s` (or `telepresence quit -ur`
if your current version is less than 2.8.0).

<Platform.Provider>
<Platform.TabGroup>
<Platform.MacOSTab>

## Upgrade with brew:
```shell
brew upgrade telepresenceio/telepresence/telepresence-oss
```

## OR upgrade by downloading the binary for your platform

### Intel Macs

```shell
# 1. Download the binary.
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

### Apple silicon Macs

```shell
# 1. Ensure that no old binary exists. This is very important because Silicon macs track the executable's signature
# and just updating it in place will not work.
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Download the binary.
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-arm64 -o /usr/local/bin/telepresence

# 3. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```
</Platform.MacOSTab>
<Platform.GNULinuxTab>

```shell
# 1. Download the latest binary (~95 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-linux-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

To upgrade Telepresence,[Click here to download the Telepresence binary](https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-windows-amd64.zip).

Once you have the binary downloaded and unzipped you will need to do a few things:

1. Rename the binary from `telepresence-windows-amd64.exe` to `telepresence.exe`
2. Move the binary to `C:\Program Files (x86)\$USER\Telepresence\`

</Platform.WindowsTab>
</Platform.TabGroup>
</Platform.Provider>

The Telepresence CLI contains an embedded Helm chart. See [Install/Uninstall the Traffic Manager](manager.md) if you want to also upgrade
the Traffic Manager in your cluster.

![scarf](https://static.scarf.sh/a.png?x-pxid=d842651a-2e4d-465a-98e1-4808722c01ab)
