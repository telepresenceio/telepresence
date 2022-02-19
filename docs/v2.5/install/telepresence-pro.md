import Platform from '@src/components/Platform';

# Install Telepresence Pro

Telepresence Pro is a replacement to Telepresence's User Daemon
that gives you premium features including:
* Creating intercepts on your local machine from Ambassador Cloud.

The `telepresence-pro` binary must be installed in the same directory as
`telepresence`. When you run `telepresence login` it will automatically be
installed and placed in the correct location. If you are in an air-gapped
environment or need to install it manually, ensure it is placed in the
correct directory.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# In this example, we install the binary in `/usr/local/bin/` since that's where `telepresence`
# is installed by default
# Intel Macs
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel-pro/darwin/amd64/$dlVersion$/latest/telepresence-pro -o /usr/local/bin/telepresence-pro
# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence-pro

# Apple silicon Macs
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel-pro/darwin/arm64/$dlVersion$/latest/telepresence-pro -o /usr/local/bin/telepresence-pro
# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence-pro
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```shell
# In this example, we install the binary in `/usr/local/bin/` since that's where `telepresence`
# is installed by default
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel-pro/linux/amd64/$dlVersion$/latest/telepresence-pro -o /usr/local/bin/telepresence-pro
# 2. Make the binary executable:
sudo chmod a+x /usr/local/bin/telepresence-pro
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```powershell
# In this example, we install the binary in `/usr/local/bin/` since that's where `telepresence`
# is installed by default
# Make sure you run the following from Powershell as Administrator
# 1. Download the latest windows zip containing telepresence-pro.exe and its dependencies (~50 MB):
curl -fL https://app.getambassador.io/download/tel-pro/windows/amd64/$dlVersion$/latest/telepresence-pro.exe -o telepresence-exe

# 2. Move the exe to your path (We recommend the default directory used by telepresence `C:\telepresence`)
Copy-Item "telepresence-pro.exe" -Destination "C:\telepresence\telepresence-pro.exe" -Force
```

</Platform.WindowsTab>
</Platform.TabGroup>

# Upgrade Telepresence Pro
There are two options to upgrade Telepresence Pro. You can run `telepresence-pro upgrade` to get the latest
version that is compatible with the current Telepresence version you are using or you can remove the binary
and Telepresence will automatically install it next time you `telepresence login`.
