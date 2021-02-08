# Changelog

### 2.1.0 (February 14, 2021)

- Telepresence is now capable of forwarding the environment variables of an intercepted service and emit them to a file as text or JSON. The environment variables will also be propagated to any command started by doing a `telepresence intercept nnn -- <command>`.

- The background processes `connector` and `daemon` will now use rotating logs and a common directory.
  + MacOS: `~/Library/Logs/telepresence/`
  + Linux: `$XDG_CACHE_HOME/telepresence/logs/` or `$HOME/.cache/telepresence/logs/`.

- A bug causing a failure in the Telepresence DNS resolver when attempting to listen to the Docker gateway IP was fix. The fix affects Windows using a combination of Docker and WSL2 only.
