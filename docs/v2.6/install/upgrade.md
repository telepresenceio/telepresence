---
description: "How to upgrade your installation of Telepresence and install previous versions."
---

import Platform from '@src/components/Platform';

# Important note about upgrading to 2.6.0
Telepresence 2.6.0 introduces a new way of configuring the traffic-agent sidecar, and will no longer modify the workloads (deployments,
replicasets, or statefulsets) in order to inject it. Instead, all sidecar injection is performed by a mutating webhook. Because of this
change, the traffic-manager will reject connections from older clients, which means that when installing a 2.6.0 traffic-manager in a
cluster, all clients must also upgrade.  The 2.6.0 client will work with older traffic-managers.

Please see [Whats new in 2.6.0](../../new-in-2.6) for more info.

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs

# Upgrade via brew:
brew upgrade datawire/blackbird/telepresence

# OR upgrade manually:
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/$dlVersion$/telepresence -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# Apple silicon Macs

# Install via brew:
brew install datawire/blackbird/telepresence-arm64

# OR Install manually:
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/arm64/$dlVersion$/telepresence -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```shell
# 1. Download the latest binary (~50 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/linux/amd64/$dlVersion$/telepresence -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```powershell
# Windows is in Developer Preview, here is how you can install it:
# Make sure you run the following from Powershell as Administrator
# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~50 MB):
curl -fL https://app.getambassador.io/download/tel2/windows/amd64/$dlVersion$/telepresence.zip -o telepresence.zip

# 2. Unzip the zip file to a suitable directory + cleanup zip
Expand-Archive -Path telepresence.zip
Remove-Item 'telepresence.zip'
cd telepresence

# 3. Run the install-telepresence.ps1 to install telepresence's dependencies. It will install telepresence to
# C:\telepresence by default, but you can specify a custom path by passing in -Path C:\my\custom\path
Set-ExecutionPolicy Bypass -Scope Process
.\install-telepresence.ps1

# 4. Remove the unzipped directory
cd ..
Remove-Item telepresence
# 5. Close your current Powershell and open a new one. Telepresence should now be usable as telepresence.exe
```

</Platform.WindowsTab>
</Platform.TabGroup>

After upgrading your CLI you must stop any live Telepresence processes by issuing `telepresence quit`, then upgrade the Traffic Manager by running `telepresence connect`

**Note** that if the Traffic Manager has been installed via Helm, `telepresence connect` will never upgrade it. If you wish to upgrade a Traffic Manager that was installed via the Helm chart, please see the [Helm documentation](../helm#upgrading-the-traffic-manager)
