---
description: "How to upgrade your installation of Telepresence and install previous versions."
---

# Upgrading Telepresence

The Telepresence CLI will periodically check for new versions and notify you when an upgrade is available.  [Running the same commands used for installation](../../quick-start/) will replace your current binary with the latest version.

### <img class="os-logo" src="../../images/apple.png"/> macOS

```
sudo curl -fL https://app.getambassador.io/download/tel2/darwin/amd64/latest/telepresence \
-o /usr/local/bin/telepresence && \
sudo chmod a+x /usr/local/bin/telepresence && \
telepresence version
```

### <img class="os-logo" src="../../images/linux.png"/> Linux

```
sudo curl -fL https://app.getambassador.io/download/tel2/linux/amd64/latest/telepresence \
-o /usr/local/bin/telepresence && \
sudo chmod a+x /usr/local/bin/telepresence && \
telepresence version
```

## Installing Older Versions of Telepresence

Use the following URLs to install an older version, replacing `x.x.x` with the version you want.

### macOS
`https://app.getambassador.io/download/tel2/linux/amd64/x.x.x/telepresence`

### Linux
`https://app.getambassador.io/download/tel2/darwin/amd64/x.x.x/telepresence`

<br/>

Use the following URLs to find the current latest version number.

### macOS
`https://app.getambassador.io/download/tel2/linux/amd64/stable.txt`

### Linux
`https://app.getambassador.io/download/tel2/darwin/amd64/stable.txt`