import Platform from '@src/components/Platform';

# Install

Install Telepresence by running the commands below for your OS. If you are not the administrator of your cluster, you will need [administrative RBAC permissions](../reference/rbac#administrating-telepresence) to install and use Telepresence in your cluster.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs

# Install via brew:
brew install datawire/blackbird/telepresence

# OR install manually:
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
# C:\telepresence by default, but you can specify a custom path $path with -Path $path
Set-ExecutionPolicy Bypass -Scope Process
.\install-telepresence.ps1

# 4. Remove the unzipped directory
cd ..
Remove-Item telepresence
# 5. Close your current Powershell and open a new one. Telepresence should now be usable as telepresence.exe
```

</Platform.WindowsTab>
</Platform.TabGroup>

## <img class="os-logo" src="../images/logo.png"/> What's Next?

Follow one of our [quick start guides](../quick-start/) to start using Telepresence, either with our sample app or in your own environment.

## Installing nightly versions of Telepresence

We build and publish the contents of the default branch, [release/v2](https://github.com/telepresenceio/telepresence), of Telepresence
nightly, Monday through Friday, for macOS (Intel and Apple silicon), Linux, and Windows.

The tags are formatted like so: `vX.Y.Z-nightly-$gitShortHash`.

`vX.Y.Z` is the most recent release of Telepresence with the patch version (Z) bumped one higher.
For example, if our last release was 2.3.4, nightly builds would start with v2.3.5, until a new
version of Telepresence is released.

`$gitShortHash` will be the short hash of the git commit of the build.

Use these URLs to download the most recent nightly build.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs
https://app.getambassador.io/download/tel2/darwin/amd64/nightly/telepresence

# Apple silicon Macs
https://app.getambassador.io/download/tel2/darwin/arm64/nightly/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
https://app.getambassador.io/download/tel2/linux/amd64/nightly/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```
https://app.getambassador.io/download/tel2/windows/amd64/nightly/telepresence.zip
```

</Platform.WindowsTab>
</Platform.TabGroup>

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS (including older nightly builds), replacing `x.y.z` with the versions you want.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Intel Macs
https://app.getambassador.io/download/tel2/darwin/amd64/x.y.z/telepresence

# Apple silicon Macs
https://app.getambassador.io/download/tel2/darwin/arm64/x.y.z/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
https://app.getambassador.io/download/tel2/linux/amd64/x.y.z/telepresence
```

</Platform.GNULinuxTab>
<Platform.WindowsTab>

```
https://app.getambassador.io/download/tel2/windows/amd64/x.y.z/telepresence
```

</Platform.WindowsTab>
</Platform.TabGroup>
