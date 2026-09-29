# Super Heroes Kafka intercept demo

Status: proposed for review. Implementation has not started.

## Demo story

Run the existing Super Heroes battle UI against the cluster. Open the cluster
statistics dashboard and a local copy side by side. Intercept only events for
fights won by heroes. Hero wins update the local dashboard; villain wins update
the cluster dashboard. Change the local statistics page in Quarkus dev mode and
show the change without deploying the statistics service.

Use the real fight producer, Kafka broker, Avro events, and schema registry.
Telepresence routes by a Kafka header; it does not inspect the Avro body.

## Findings from the current checkout and running app

- `setup-superheroes.sh` has deployed a healthy app in the `superheroes` kind
  cluster and namespace. It creates its own kubeconfig, so this cluster is not
  currently in the user's normal kubeconfig.
- Telepresence is connected to `kind-dev`. The Super Heroes cluster has no
  traffic-manager or Kafka provider installed yet.
- `rest-fights` publishes Avro events to `fights`. Its producer currently sends
  the payload without an explicit key or custom routing header.
- Each event already contains `winnerTeam`, normally `heroes` or `villains`.
- `event-statistics` consumes `fights`, uses Apicurio, and keeps statistics in
  memory. Its topic is set in application properties. Topic, group, and isolation
  level are not explicit literal environment entries in its Deployment.
- KafkaSplit requires those three explicit environment entries. Establish and
  verify the existing consumer group before enabling the split; do not assume
  its name or accidentally start a new group.
- The broker manifest sets transaction state replication and minimum ISR to 1,
  suitable for its single broker. Verify transaction readiness in the rehearsal.
- Java 25 is installed, matching the local application's build configuration.
- Setup forwards the cluster dashboard to port 8085. Use port 9085 for the local
  dashboard. Enabling or disabling a split replaces statistics Pods, resets
  their in-memory statistics, and can stop the existing port-forward.

## Proposed implementation

1. Add a guide under `examples/kafka/README.md` with a short presentation script,
   explicit preparation steps, commands, expected results, and cleanup.
2. Add a small, reviewable patch under `examples/kafka/patches/` for the upstream
   fight producer. Attach a UTF-8 `winner-team` Kafka header from the existing
   event field. Preserve the Avro payload and schema. Check that the patch applies
   before modifying a checkout, and retain any user edits. Record the tested
   upstream commit and image digests. Build and load only the patched producer
   image into kind. Do not vendor the upstream checkout into Telepresence.
3. Add a separate preparation helper for the Kafka demo. Reuse the setup
   script's cluster and namespace options and isolated kubeconfig approach.
   Verify required tools and workloads, use branch-built Telepresence client,
   manager/agent and Kafka provider images, and install with `kafka.enabled=true`.
   Connect explicitly to the demo cluster. Explain that this changes the active
   host Telepresence connection.
4. Make the consumer configuration explicit in `event-statistics`:
   `MP_MESSAGING_INCOMING_FIGHTS_TOPIC`,
   `MP_MESSAGING_INCOMING_FIGHTS_GROUP_ID`, and
   `MP_MESSAGING_INCOMING_FIGHTS_ISOLATION_LEVEL`. Keep the original topic and
   verified group, and use `read_committed`. Wait for a healthy consumer before
   applying the split.
5. Add a KafkaSplit manifest selecting `app: event-statistics`, container
   `event-statistics`, source topic `fights`, and the verified group. Bind the
   three environment variables above. Use managed shadow topics and one splitter
   replica. Verify broker advertised addresses resolve from the provider,
   splitter, and local consumer; bootstrap addresses alone are insufficient.
6. Add a local launch helper and document the central command:

   ```sh
   telepresence intercept hero-stats \
     --workload event-statistics --namespace superheroes \
     --kafka-only --kafka-header winner-team=heroes \
     -- <local Quarkus launch helper>
   ```

   The helper runs the upstream statistics module in dev mode on port 9085,
   using the intercept-provided environment. It uses cluster Kafka and Apicurio,
   disables automatic local infrastructure and unused telemetry, and preserves
   the personal topic, group, and isolation values supplied by Telepresence.
   Verify configuration precedence, including dotted variables from `envFrom`.
   An existing-image local run can be documented if preferred over dev mode.
7. Document safe teardown: stop the local consumer, leave the intercept, wait
   for its route to finish draining, disable the split, and wait for `Disabled`.
   Restore demo-specific producer/configuration changes only after the split
   has handed consumption back. Keep the app and cluster available for another
   run. Do not delete Kafka topics manually.

## Presentation sequence

1. Before the split, make a fight and show the cluster statistics update.
2. Prepare the split and restart the setup port-forwards if necessary. Explain
   that the statistics reset because the service stores them in memory.
3. Start the personal intercept and local statistics service. Show the filter
   and the reported Kafka split attachment.
4. Generate fights in the battle UI until both teams have won. Show hero wins
   only on the local statistics dashboard and villain wins only on the cluster
   dashboard. Provide repeatable API requests for rehearsal if random fights
   make the recording too slow.
5. Make a small visible local page change, reload, and generate another hero win.
   The cluster page stays unchanged.
6. Stop the local consumer and leave the intercept. New events for both teams
   reach the cluster consumer again. Consumed personal events are not replayed;
   unconsumed personal events are returned during route cleanup.

## Verification and completion

- Delegate implementation to a suitable lower-power coding worker, with explicit
  ownership of `examples/kafka/` demo additions and any upstream patch work.
- Rehearse against the running demo cluster after plan review. Check both
  matching and nonmatching events by identity, not only dashboard totals.
- Verify the original group, active route, local effective configuration, and
  cluster effective configuration. Do not accept a local consumer that merely
  reads the source topic in a second group as a successful intercept demo.
- Verify Avro decoding through Apicurio from the local process.
- Exercise cleanup and a second run. Account for Pod replacement, lost
  port-forwards, in-memory statistics resets, and local group shutdown.
- Run shell syntax checks, shellcheck, relevant producer checks, and `make lint`
  before pushing. Keep generated environment/configuration and cloned sources
  out of commits.
- Preserve the user's staged setup script and unrelated untracked files.
- Remove this plan when the demo is implemented and documented.
