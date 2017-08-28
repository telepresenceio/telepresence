<script src="https://cdn.jsdelivr.net/npm/clipboard@1/dist/clipboard.min.js"></script>
<script>
    var clipboard = new Clipboard('.copy-to-clipboard');
    clipboard.on('success', function(e) {
        ga('send', 'event', 'telepresence', 'download', e.trigger.dataset.system);
        e.clearSelection();
        alert('Copied to clipboard!');
    });
</script>

#### OS X
<div class="u-cf u-full-width">
On OS X you can install Telepresence by running the following:
<button id="osxInstall" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="brew cask install osxfuse&#xa;brew install datawire/blackbird/telepresence">Copy to clipboard</button>
</div>
```shell
brew cask install osxfuse
brew install datawire/blackbird/telepresence
```

#### Ubuntu 16.04 or later
<div class="u-cf u-full-width">
Run the following to install Telepresence:
<button id="ubuntuInstall" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.deb.sh | sudo bash&#xa;sudo apt install --no-install-recommends telepresence">Copy to clipboard</button>
</div>
```shell
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.deb.sh | sudo bash
sudo apt install --no-install-recommends telepresence
```

#### Fedora 25
<div class="u-cf u-full-width">
Run the following:
<button id="fedoraInstall" class="button fa-pull-right copy-to-clipboard" data-clipboard-text="curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.rpm.sh | sudo bash&#xa;sudo dnf install telepresence">Copy to clipboard</button>
</div>
```shell
curl -s https://packagecloud.io/install/repositories/datawireio/telepresence/script.rpm.sh | sudo bash
sudo dnf install telepresence
```

#### Windows

See the [Windows support documentation](/reference/windows.html).

#### Other platforms

Don't see your favorite platform?
[Let us know](https://github.com/datawire/telepresence/issues/new) and we'll try to add it. 
