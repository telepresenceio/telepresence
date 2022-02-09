---
description: "Troubleshooting issues related to Telepresence."
---
# Troubleshooting

## Creating an intercept did not generate a preview URL

Preview URLs can only be created if Telepresence is [logged in to
Ambassador Cloud](../reference/client/login/).  When not logged in, it
will not even try to create a preview URL (additionally, by default it
will intercept all traffic rather than just a subset of the traffic).
Remove the intercept with `telepresence leave [deployment name]`, run
`telepresence login` to login to Ambassador Cloud, then recreate the
intercept.  See the [intercepts how-to doc](../howtos/intercepts) for
more details.

## Error on accessing preview URL: `First record does not look like a TLS handshake`

The service you are intercepting is likely not using TLS, however when configuring the intercept you indicated that it does use TLS. Remove the intercept with `telepresence leave [deployment name]` and recreate it, setting `TLS` to `n`. Telepresence tries to intelligently determine these settings for you when creating an intercept and offer them as defaults, but odd service configurations might cause it to suggest the wrong settings.

## Error on accessing preview URL: Detected a 301 Redirect Loop

If your ingress is set to redirect HTTP requests to HTTPS and your web app uses HTTPS, but you configure the intercept to not use TLS, you will get this error when opening the preview URL.  Remove the intercept with `telepresence leave [deployment name]` and recreate it, selecting the correct port and setting `TLS` to `y` when prompted.

## Connecting to a cluster via VPN doesn't work.

There are a few different issues that could arise when working with a VPN. Please see the [dedicated page](../reference/vpn) on Telepresence and VPNs to learn more on how to fix these.

## Your GitHub organization isn't listed

Ambassador Cloud needs access granted to your GitHub organization as a
third-party OAuth app.  If an organization isn't listed during login
then the correct access has not been granted.

The quickest way to resolve this is to go to the **Github menu** →
**Settings** → **Applications** → **Authorized OAuth Apps** →
**Ambassador Labs**.  An organization owner will have a **Grant**
button, anyone not an owner will have **Request** which sends an email
to the owner.  If an access request has been denied in the past the
user will not see the **Request** button, they will have to reach out
to the owner.

Once access is granted, log out of Ambassador Cloud and log back in;
you should see the GitHub organization listed.

The organization owner can go to the **GitHub menu** → **Your
organizations** → **[org name]** → **Settings** → **Third-party
access** to see if Ambassador Labs has access already or authorize a
request for access (only owners will see **Settings** on the
organization page).  Clicking the pencil icon will show the
permissions that were granted.

GitHub's documentation provides more detail about [managing access granted to third-party applications](https://docs.github.com/en/github/authenticating-to-github/connecting-with-third-party-applications) and [approving access to apps](https://docs.github.com/en/github/setting-up-and-managing-organizations-and-teams/approving-oauth-apps-for-your-organization).

### Granting or requesting access on initial login

When using GitHub as your identity provider, the first time you log in
to Ambassador Cloud GitHub will ask to authorize Ambassador Labs to
access your organizations and certain user data.

<img src="../images/github-login.png" width="50%"/>

Any listed organization with a green check has already granted access
to Ambassador Labs (you still need to authorize to allow Ambassador
Labs to read your user data and organization membership).

Any organization with a red "X" requires access to be granted to
Ambassador Labs.  Owners of the organization will see a **Grant**
button.  Anyone who is not an owner will see a **Request** button.
This will send an email to the organization owner requesting approval
to access the organization.  If an access request has been denied in
the past the user will not see the **Request** button, they will have
to reach out to the owner.

Once approval is granted, you will have to log out of Ambassador Cloud
then back in to select the organization.

### Volume mounts are not working on macOS

It's necessary to have `sshfs` installed in order for volume mounts to work correctly during intercepts. Lately there's been some issues using `brew install sshfs` a macOS workstation because the required component `osxfuse` (now named `macfuse`) isn't open source and hence, no longer supported. As a workaround, you can now use `gromgit/fuse/sshfs-mac` instead. Follow these steps:

1. Remove old sshfs, macfuse, osxfuse using `brew uninstall`
2. `brew install --cask macfuse`
3. `brew install gromgit/fuse/sshfs-mac`
4. `brew link --overwrite sshfs-mac`

Now sshfs -V shows you the correct version, e.g.:
```
$ sshfs -V
SSHFS version 2.10
FUSE library version: 2.9.9
fuse: no mount point
```

but one more thing must be done before it works OK:
5. Try a mount (or an intercept that performs a mount). It will fail because you need to give permission to “Benjamin Fleischer” to execute a kernel extension (a pop-up appears that takes you to the system preferences).
6. Approve the needed permission
7. Reboot your computer.
