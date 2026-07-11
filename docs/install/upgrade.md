---
title: Upgrade client
description: "How to upgrade your installation of Telepresence."
hide_table_of_contents: true
---

# Upgrade Process

The Telepresence CLI periodically checks for new versions and notifies you when
an upgrade is available. Upgrading is done by installing the latest version the
same way you installed the current one: follow the steps on the
[install page](client.md) and the new version will replace the old.

Before upgrading, stop any live Telepresence processes with
`telepresence quit -s`.

A few notes:

- The package installers (the macOS `.pkg`, the Linux `.deb`/`.rpm` packages,
  and the Windows setup installer) replace the previous version and restart
  the root daemon service.
- Homebrew users upgrade with
  `brew upgrade telepresenceio/telepresence/telepresence-oss`.
- When replacing a manually downloaded binary on an Apple silicon Mac, remove
  the old binary first (`sudo rm -f /usr/local/bin/telepresence`). macOS
  tracks the executable's signature, and updating the file in place will not
  work.

The Telepresence CLI contains an embedded Helm chart. See
[Install/Uninstall the Traffic Manager](manager.md) if you also want to
upgrade the Traffic Manager in your cluster.
