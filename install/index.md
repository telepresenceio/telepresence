import Platform from '@src/components/Platform';

# Install

Install Telepresence by running the commands below for your OS.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# 1. Download the latest binary (~60 MB):
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/$dlVersion$/telepresence -o /usr/local/bin/telepresence

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
</Platform.TabGroup>

## <img class="os-logo" src="../images/logo.png"/> What's Next?

Follow one of our [quick start guides](../quick-start/) to start using Telepresence, either with our sample app or in your own environment.

## Installing older versions of Telepresence

Use these URLs to download an older version for your OS, replacing `x.y.z` with the versions you want.

<Platform.TabGroup>
<Platform.MacOSTab>

```
https://app.getambassador.io/download/tel2/darwin/amd64/x.y.z/telepresence
```

</Platform.MacOSTab>
<Platform.GNULinuxTab>

```
https://app.getambassador.io/download/tel2/linux/amd64/x.y.z/telepresence
```

</Platform.GNULinuxTab>
</Platform.TabGroup>
