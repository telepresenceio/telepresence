# Telepresence Login

```console
$ telepresence login --help
Authenticate to Ambassador Cloud

Usage:
  telepresence login [flags]

Flags:
      --apikey string   Static API key to use instead of performing an interactive login
```

## Description

Use `telepresence login` to explicitly authenticate with [Ambassador
Cloud](https://www.getambassador.io/docs/cloud).  Unless the
[`skipLogin` option](../../config) is set, other commands will
automatically invoke the `telepresence login` interactive login
procedure as nescessary, so it is rarely nescessary to explicitly run
`telepresence login`; it should only be truly nescessary to explictly
run `telepresence login` when you require a non-interactive login.

The normal interactive login procedure involves launching a web
browser, a user interacting with that web browser, and finally having
the web browser make callbacks to the local Telepresence process.  If
it is not possible to do this (perhaps you are using a headless remote
box via SSH, or are using Telepresence in CI), then you may instead
have Ambassador Cloud issue an API key that you pass to `telepresence
login` with the `--apikey` flag.

## Telepresence Pro

When you run `telepresence login`, the CLI will recommend you install
a Telepresence Pro binary.  The Telepresence Pro version of the [User
Daemon](../../architecture) communicates with the Ambassador Cloud to
provide fremium features including the ability to create intercepts from
Ambassador Cloud.

(show a screenshot of what the login flow looks like... but for now
I'll do a code snippet)
```
telepresence login
Telepresence wants to install the Telepresence Pro User Daemon to enable Cloud features(y|n): y
Telepresence Pro installed!
Launching Telepresence Pro User Daemon
Launching browser authentication flow...
Login successful.
```

## Acquiring an API key

1. Log in to Ambassador Cloud at https://app.getambassador.io/ .

2. Click on your profile icon in the upper-left: ![Screenshot with the
   mouse pointer over the upper-left profile icon](./apikey-2.png)

3. Click on the "API Keys" menu button: ![Screenshot with the mouse
   pointer over the "API Keys" menu button](./apikey-3.png)

4. Click on the "generate new key" button in the upper-right:
   ![Screenshot with the mouse pointer over the "generate new key"
   button](./apikey-4.png)

5. Enter a description for the key (perhaps the name of your laptop,
   or perhaps the "CI"), and click "generate api key" to create it.

You may now pass the API key as `KEY` to `telepresence login --apikey=KEY`.

Telepresence will use that "master" API key to create narrower keys
for different components of Telepresence.  You will see these appear
in the Ambassador Cloud web interface.
