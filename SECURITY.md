# Security Policy

## Supported Versions

Security updates will be provided for the latest 2.x release.


### How do we handle vulnerabilities

#### User reports

If you discover any security vulnerabilities, please follow these guidelines:

- Email your findings to [secalert@datawire.io](secalert@datawire.io).
- Provide sufficient details, including steps to reproduce the vulnerability.
- Do not publicly disclose the issue until we have had a chance to address it.

#### Dependabot

We run dependabot against our repo. We also have it create PRs with the updates. 

One of the maintainers responsibilities is to review these PRs, make any necessary updates, 
and merge them in so that they go out in our next set of releases.

#### Keeping Go updated

We're set up to receive embargoed security announcements for Golang. When it happens, 
we create a new security incident, evaluate if we're impacted, and release a hotfix as soon as possible.

