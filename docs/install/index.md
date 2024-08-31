import Platform from '@src/components/Platform';

# Install

Install Telepresence OSS by running the commands below for your OS. If you are not the administrator of your cluster, you will need [administrative RBAC permissions](../reference/rbac#administrating-telepresence) to install and use Telepresence in your cluster.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs

# 1. Download the latest binary (~105 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-amd64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence

# Apple silicon Macs

# 1. Download the latest binary (~101 MB):
sudo curl -fL https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-darwin-arm64 -o /usr/local/bin/telepresence

# 2. Make the binary executable:
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

We've developed a Powershell script to simplify the process of installing telepresence. Here are the commands you can execute:

```powershell
# To install Telepresence, run the following commands
# from PowerShell as Administrator.

# 1. Download the latest windows zip containing telepresence.exe and its dependencies (~50 MB):
Invoke-WebRequest https://app.getambassador.io/download/tel2oss/releases/download/$dlVersion$/telepresence-windows-amd64.zip -OutFile telepresence.zip

# 2. Unzip the telepresence.zip file to the desired directory, then remove the zip file:
Expand-Archive -Path telepresence.zip -DestinationPath telepresenceInstaller/telepresence
Remove-Item 'telepresence.zip'
cd telepresenceInstaller/telepresence

# 3. Run the install-telepresence.ps1 to install telepresence's dependencies. It will install telepresence to
# C:\telepresence by default, but you can specify a custom path by passing in -Path C:\my\custom\path
powershell.exe -ExecutionPolicy bypass -c " . '.\install-telepresence.ps1';"

# 4. Remove the unzipped directory:
cd ../..
Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force

# 5. Telepresence is now installed and you can use telepresence commands in PowerShell.
```

</Platform.WindowsTab>
</Platform.TabGroup>

## <img class="os-logo" src="../images/logo.png" alt="Telepresence logo" /> What's Next?

Follow one of our [quick start guides](../quick-start/) to start using Telepresence, either with our sample app or in your own environment.

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS (including older nightly builds), replacing `x.y.z` with the versions you want.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs
https://app.getambassador.io/download/tel2oss/releases/download/vx.y.z/telepresence-darwin-amd64

# Apple silicon Macs
https://app.getambassador.io/download/tel2oss/releases/download/vx.y.z/telepresence-darwin-arm64
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
https://app.getambassador.io/download/tel2oss/releases/download/vx.y.z/telepresence-linux-amd64
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```
(https://app.getambassador.io/download/tel2oss/releases/download/vx.y.z/telepresence-windows-amd64.exe
```

</Platform.WindowsTab>
</Platform.TabGroup>

<img referrerpolicy="no-referrer-when-downgrade" src="https://static.scarf.sh/a.png?x-pxid=d842651a-2e4d-465a-98e1-4808722c01ab" alt="" style="max-width:1px;"/>
