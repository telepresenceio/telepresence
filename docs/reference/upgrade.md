---
layout: doc
weight: 8
title: "Upgrading Telepresence"
categories: reference
---

<script src="https://cdn.jsdelivr.net/npm/clipboard@1/dist/clipboard.min.js"></script>
<script>
    var clipboard = new Clipboard('.copy-to-clipboard');
    clipboard.on('success', function(e) {
        $(e.trigger).text('Copied');
        e.clearSelection();
    });
</script>

#### OS X
<div class="u-cf u-full-width">
On OS X you can upgrade Telepresence by running the following:
<button data-system="osx" data-location="{{ include.location }}" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="brew upgrade datawire/blackbird/telepresence">Copy to clipboard</button>
</div>
```shell
brew upgrade datawire/blackbird/telepresence
```

#### Ubuntu 16.04 or later
<div class="u-cf u-full-width">
Run the following to upgrade Telepresence:
<button data-system="ubuntu" data-location="{{ include.location }}" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="sudo apt update&#xa;sudo apt install --no-install-recommends telepresence">Copy to clipboard</button>
</div>
```shell
sudo apt update
sudo apt install --no-install-recommends telepresence
```

#### Fedora 25
<div class="u-cf u-full-width">
Run the following to upgrade Telepresence:
<button data-system="fedora" data-location="{{ include.location }}" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="sudo dnf upgrade telepresence">Copy to clipboard</button>
</div>
```shell
sudo dnf upgrade telepresence
```

### More

Take a look at the [changelog](changelog) to see what's new.

See what others are up to on the [community page](community).

Get involved! Find us in the [Gitter chatroom](https://gitter.im/datawire/telepresence) or [submit a pull request](https://github.com/datawire/telepresence/pulls).
