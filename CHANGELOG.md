## CHANGELOG

## 0.5.1 (2019-05-21)

 * [watt] Improved error reporting for unrecognized kubernetes resources.

## 0.5.0 (2019-05-20)

 * [teleproxy] Added support for intercepting specific ports rather than just blanket ip addresses.

## 0.4.12 (2019-05-17)

 * [watt] Only invoke the watch hook when we are bootstrapped with respect to our initial set of sources.
 * [lib/supervisor] Added Process.DoClean().
 * [lib/supervisor] Added smart (rate limited) logging of workers that are blocked.

## 0.4.11 (2019-05-16)

 * [teleproxy] Added self check to avoid starting up despite not functioning properly.
 * [teleproxy] Bug fix: internal startup race

## 0.4.10 (2019-05-08)

 * [watt] Bug fix: interpolated addresses cause watt to never reach bootstrapped stage [teleproxy#110](datawire/teleproxy#110)

## 0.4.9 (2019-05-06)

 * [teleproxy] Made teleproxy log start with a legend for what different prefixes mean, so its more self documenting.
 * [teleproxy] Switch teleproxy over to using Supervisor library for increased startup/shutdown robustness.
 * [teleproxy] Removed all known occurrances of fatals and exits from teleproxy code in order to increase robustness of firewall cleanup on exit.
 * [teleproxy] Fixed docker integration to work with more recent versions of docker.
 * [teleproxy] Bug fix: log all dns queries (not just ones we intercept).
 * [lib/supervisor] Added supervisor support for launching subprocesses in a way that automatically logs input, output, and exit codes.
 * [lib/supervisor] Made supervisor logging less noisy and more consistently formatted.
 * [lib/supervisor] Added supervisor.Run and supervisor.MustRun for convenience.
 * [lib/supervisor] Added delay to supervisor's retry behavior.
 * [lib/supervisor] Bug fix: recover from panic inside Process.Do.

## 0.4.8 (2019-04-26)

 * [watt] Add an index page to watt's snapshot server for easier debugging.

## 0.4.7 (2019-04-26)

 * [watt] Add support for environment variables in consul's address field.
 * [watt] Bug fix: only keep around 10 snapshots instead of all of them.

## 0.4.6 (2019-04-22)

 * [teleproxy] Add flag to disable dns search override.

## 0.4.5 (2019-04-22)

 * [teleproxy] Bug fix: pay attention to more exit codes from subprocesses.

## 0.4.4 (2019-04-22)

 * [teleproxy] Bug fix: don't ignore errors from system integration code on OSX.

## 0.4.3 (2019-04-22)

 * [teleproxy] Bug fix: shutdown cleanup didn't happen due to log.Fatal.

## 0.4.2 (2019-04-18)

 * [watt] Bug fix: consul configurations with more than one service failed to bootstrap properly.

## 0.4.1 (2019-04-16)

 * [watt] Add watt to the set of released binaries.

## 0.4.0 (2019-04-16)

 * [watt] Added watt binary in favor of kubewatch. Supports consul as
          a discovery source in addition to kubernetes.
 * [kubewatch] Kubewatch is deprecated in favor of watt.
 * [lib] Added supervisor package.
