# Docker network plugin for Telepresence

## Debugging

Start by configuring telepresence to not check for the latest version of the plugin, but instead use our debug version by
adding the following yaml to the `config.yml` (on Linux, this will be in `~/.config/telepresence/config.yml`, and on mac
you'll find it in `"$HOME/Library/Application Support/telepresence/config.yml"`:
```yaml
intercept:
  teleroute:
    tag: debug
```

Build the plugin for debugging. The command both builds and enables the plugin:
```console
$ make debug
```

Use runc to tail the plugin's log output

```console
sudo runc --root /run/docker/runtime-runc/plugins.moby exec $(docker plugin list --no-trunc -f capability=networkdriver -f enabled=true -q) tail -n 400 -f /var/log/teleroute.log
```
