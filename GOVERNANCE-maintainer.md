# Telepresence Project Governance

The goal of the Telepresence project is to accelerate the "developer inner loop" for cloud-native application
development on Kubernetes.

Telepresence achieves this by creating a dynamic network bridge between a local development environment (e.g., a laptop)
and a remote Kubernetes cluster.

This governance explains how the project is run.

- [Values](#values)
- [Maintainers](#maintainers)
- [Becoming a Maintainer](#becoming-a-maintainer)
- [Meetings](#meetings)
- [CNCF Resources](#cncf-resources)
- [Code of Conduct Enforcement](#code-of-conduct)
- [Security Response Team](#security-response-team)
- [Voting](#voting)
- [Modifications](#modifying-this-charter)

## Values

The Telepresence project and its leadership embrace the following values:

* Openness: Communication and decision-making happens in the open and is discoverable for future
  reference. As much as possible, all discussions and work take place in public
  forums and open repositories.

* Fairness: All stakeholders have the opportunity to provide feedback and submit
  contributions, which will be considered on their merits.

* Community over Product or Company: Sustaining and growing our community takes
  priority over shipping code or sponsors' organizational goals.  Each
  contributor participates in the project as an individual.

* Inclusivity: We innovate through different perspectives and skill sets, which
  can only be accomplished in a welcoming and respectful environment.

* Participation: Responsibilities within the project are earned through
  participation, and there is a clear path up the contributor ladder into leadership
  positions.

## Maintainers

Telepresence Maintainers have write access to the [project GitHub repository](https://github.com/telepresenceio/telepresence).
They can merge their own patches or patches from others. The current maintainers
can be found in [MAINTAINERS.md](./MAINTAINERS.md).  Maintainers collectively manage the project's
resources and contributors.

This privilege is granted with some expectation of responsibility: maintainers
are people who care about the Telepresence project and want to help it grow and
improve. A maintainer is not just someone who can make changes, but someone who
has demonstrated their ability to collaborate with the team, get the most
knowledgeable people to review code and docs, contribute high-quality code, and
follow through to fix issues (in code or tests).

A maintainer is a contributor to the project's success and a citizen helping
the project succeed.

The collective team of all Maintainers is known as the Maintainer Council, which
is the governing body for the project.

### Maintainer responsibilities

* Monitor email aliases.
* Monitor Slack (delayed response is perfectly acceptable).
* Triage GitHub issues and perform pull request reviews for other maintainers and the community.
* Make sure that ongoing PRs are moving forward at the right pace or closing them.
* In general continue to be willing to spend at least 20% of one's time working on Telepresence (~1 business day/week).

### Becoming a Maintainer

* Express interest to the current maintainers (see [MAINTAINERS.md](MAINTAINERS.md)) that your organization is
interested in becoming a maintainer. Becoming a maintainer generally means that you are going to be spending
substantial time on Telepresence for the foreseeable future.
* We will expect you to start contributing increasingly complicated PRs, under the guidance of the existing maintainers.
* We may ask you to do some PRs from our backlog.
* As you gain experience with the code base and our standards, we will ask you to do code reviews for incoming PRs.
All maintainers are expected to shoulder a proportional share of community reviews.
* After a period of approximately 2-3 months of working together and making sure we see eye to eye, the existing
maintainers will confer and decide whether to grant maintainer status or not.
We make no guarantees on the length of time this will take, but 2-3 months is the goal.

A new Maintainer can apply by sending a message in our [OSS Slack workspace](https://communityinviter.com/apps/cloud-native/cncf),
in the [#telepresence-oss](https://cloud-native.slack.com/archives/C06B36KJ85P) channel.

A simple majority vote of existing Maintainers approves the application.

Maintainers nominations will be evaluated without prejudice
to employer or demographics.

Maintainers who are selected will be granted the necessary GitHub rights.

### Removing a Maintainer

Maintainers may resign at any time if they feel that they will not be able to
continue fulfilling their project duties.

Maintainers may also be removed after being inactive, failure to fulfill their
Maintainer responsibilities, violating the Code of Conduct, or other reasons.
Inactivity is defined as a period of very low or no activity in the project
for a year or more, with no definite schedule to return to full Maintainer
activity.

A Maintainer may be removed at any time by a 2/3 vote of the remaining maintainers.

Depending on the reason for removal, a Maintainer may be converted to Emeritus
status. Emeritus Maintainers will still be consulted on some project matters,
and can be rapidly returned to Maintainer status if their availability changes.

## Meetings

Time zones permitting, Maintainers are expected to participate in the public
developer meeting.

Details can be found [here](./MEETING_SCHEDULE.md#monthly-contributors-meeting).

Maintainers will also have closed meetings in order to discuss security reports
or Code of Conduct violations.  Such meetings should be scheduled by any
Maintainer on receipt of a security issue or CoC report.  All current Maintainers
must be invited to such closed meetings, except for any Maintainer who is
accused of a CoC violation.

## CNCF Resources

Any Maintainer may suggest a request for CNCF resources, either in the
[#telepresence-dev](https://datawire-oss.slack.com/archives/CC5D1UTTN) in slack, or during a
meeting.  A simple majority of Maintainers approves the request.  The Maintainers
may also choose to delegate working with the CNCF to non-Maintainer community
members, who will then be added to the [CNCF's Maintainer List](https://github.com/cncf/foundation/blob/main/project-maintainers.csv)
for that purpose.

## Code of Conduct

[Code of Conduct](./code-of-conduct.md)
violations by community members will be discussed and resolved
on the [private slack channel](https://datawire-oss.slack.com/archives/C061Q45SU4F).  If a Maintainer is directly involved
in the report, the Maintainers will instead designate two Maintainers to work
with the CNCF Code of Conduct Committee in resolving it.

## Security Response Team

The Maintainers will appoint a Security Response Team to handle security reports.
This committee may simply consist of the Maintainer Council themselves.  If this
responsibility is delegated, the Maintainers will appoint a team of at least two
contributors to handle it.  The Maintainers will review who is assigned to this
at least once a year.

The Security Response Team is responsible for handling all reports of security
holes and breaches according to the [security policy](./SECURITY.md).

## Voting

In general, we prefer that technical issues and maintainer membership are amicably worked out between the persons
involved. If a dispute cannot be decided independently, the maintainers can be called in to decide an issue. If the
maintainers themselves cannot decide an issue, the issue will be resolved by voting.
The voting process is a simple majority in which each maintainer receives one vote.

A vote can be taken on [#telepresence-dev](https://datawire-oss.slack.com/archives/CC5D1UTTN) or
[#telepresence-dev-private](https://datawire-oss.slack.com/archives/C061Q45SU4F) for security or conduct matters.

Any Maintainer may demand a vote be taken.

Most votes require a simple majority of all Maintainers to succeed, except where
otherwise noted.  Two-thirds majority votes mean at least two-thirds of all
existing maintainers.

## Modifying this Charter

Changes to this Governance and its supporting documents may be approved by
a 2/3 vote of the Maintainers.

## Adding new projects to the Telepresence GitHub organization

New projects will be added to the Telepresence organization via GitHub issue discussion in one of the existing projects
in the organization. Once sufficient discussion has taken place (~3-5 business days but depending on the volume
of conversation), the maintainers of *the project where the issue was opened* (since different projects in the
organization may have different maintainers) will decide whether the new project should be added. See the section
above on voting if the maintainers cannot easily decide.
