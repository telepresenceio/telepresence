---
description: "How to upgrade your installation of Telepresence and install previous versions."
---

import Platform from '@src/components/Platform';

# Upgrade Process
The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  Running the same commands used for installation will replace your current binary with the latest version.

<Platform.TabGroup>
<Platform.MacOSTab>

```shell
# Upgrade via brew:
brew upgrade datawire/blackbird/telepresence

# OR upgrade manually:
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

After upgrading your CLI, the Traffic Manager **must be uninstalled** from your cluster. This can be done using `telepresence uninstall --everything` or by `kubectl delete svc,deploy -n ambassador traffic-manager`. The next time you run a `telepresence` command it will deploy an upgraded Traffic Manager.
