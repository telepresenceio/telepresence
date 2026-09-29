# Kafka intercept demo: Super Heroes

This demo sends hero wins to a local Quarkus statistics service. Villain wins
continue to the statistics service in the cluster. Telepresence routes records
by the `winner-team` Kafka header. The Avro event and Apicurio schema stay the
same. The local service runs in Quarkus dev mode, so a page edit appears after
a browser reload.

You need Bash, Java 25, Docker, kind, kubectl, Python 3, and Maven Wrapper
support in the upstream checkout. You also need this branch's `telepresence`
client and the optional Kafka provider image. The scripts check their own
required tools before changing the cluster.

## Prepare the application and images

Run [setup-superheroes.sh](setup-superheroes.sh) first. Its default kind
cluster and namespace are both `superheroes`. Keep its terminal open to serve
the battle UI at `http://localhost:8080`, fight API at `:8082`, and cluster
statistics at `:8085`. Keep the cluster statistics page open: this sample
starts its Kafka subscriber only while dashboard WebSockets are connected.
Both `/stats/winners` and `/stats/team` must be subscribed before cutover.

The example checkout is `examples/kafka/quarkus-super-heroes` by default.
For a repeatable recording, use upstream commit
`c32c56a90927c015850b237ecd5c26ee559ec827`. The image digests used for
the initial rehearsal are recorded in the table below. The setup script's
`java25-latest` tags are mutable.

The producer patch adds the UTF-8 header from the existing `winnerTeam` field.
Apply it only to a checkout where `git apply --check` passes; this leaves other
checkout edits alone. If it is already applied, skip the two `git apply`
commands. The patch does not change the Avro schema.

```sh
cd examples/kafka/quarkus-super-heroes
git apply --check ../patches/rest-fights-winner-team.patch
git apply ../patches/rest-fights-winner-team.patch
./mvnw -pl rest-fights -am package -DskipTests
docker build -f rest-fights/src/main/docker/Dockerfile.jvm \
  -t local/rest-fights:kafka-demo rest-fights
kind load docker-image --name superheroes local/rest-fights:kafka-demo
```

Build the Telepresence client and the two cluster images from this branch.
The tag used here is `2.33.0-kafka-demo.1` (the build variable has a leading
`v`). Use Java 25 for the producer build.

```sh
cd ../../..
TELEPRESENCE_VERSION=v2.33.0-kafka-demo.1 TELEPRESENCE_REGISTRY=local \
  make build tel2-image kafka-image
kind load docker-image --name superheroes \
  local/tel2:2.33.0-kafka-demo.1 \
  local/telepresence-kafka:2.33.0-kafka-demo.1
```

The original statistics group is `event-statistics`. Verify this on a fresh
cluster while both dashboard WebSockets are open: check the statistics Pod log
for the consumer group and list groups on the Kafka broker. Do not enable a
split with a guessed group. `prepare-demo.sh` requires the verified value.

```sh
bash examples/kafka/prepare-demo.sh \
  --group event-statistics \
  --producer-image local/rest-fights:kafka-demo
```

The preparation script saves the original Deployment configuration under
`examples/kafka/.demo-state/` (ignored by Git), loads the exact images, installs
or upgrades the manager with `kafka.enabled=true`, connects the branch client,
sets three explicit statistics consumer variables, and enables the `KafkaSplit`.
The manager and provider run in `superheroes` because the Kafka broker advertises
the short name `fights-kafka:9092`. The provider, splitter, and local consumer
must be able to resolve that advertised address. The script stops the active
Telepresence host connection before connecting to this demo cluster. If you
already installed the provider in `superheroes`, add `--manager-installed`.

Enabling the split replaces statistics Pods, which clears their in-memory
counts. The old statistics port-forward can stop. Restart it with
`bash examples/kafka/setup-superheroes.sh --forward-only`, then reload the
dashboard. Keep the dashboard open while the split drains into the cluster
application shadow. Wait for `KafkaSplit/hero-stats` phase `Enabled` before
starting the intercept. If preparation times out, inspect the printed split
status and Pod list. Keep `.demo-state/` intact and run `cleanup-demo.sh` to
disable the split and restore the Deployment settings before retrying. Do not
start a personal intercept while the split is still changing Pods.

Stop the old `setup-superheroes.sh` terminal before running `--forward-only`.
The UI and API forwards can still be alive after the statistics forward has
stopped, so running a second copy without stopping the first can leave its
ports occupied.

## Present the split

Open the battle UI, cluster statistics at `http://localhost:8085`, and later
local statistics at `http://localhost:9085`. In a new terminal at the repo root:

```sh
build-output/bin/telepresence intercept hero-stats \
  --workload event-statistics --namespace superheroes \
  --kafka-only --kafka-header winner-team=heroes \
  -- examples/kafka/run-local-stats.sh
```

The command should report the Kafka split attachment and start Quarkus dev
mode. The local process inherits its personal shadow topic, group, and
`read_committed` isolation from Telepresence. The launcher supplies the demo
cluster's Kafka address (`fights-kafka:9092`) and Apicurio registry addresses
(`apicurio:8080`) as standard uppercase variables. The sample's ConfigMap
uses dotted keys that the intercept does not pass to the local process. You
can override the uppercase variables for a different deployment. Do not
replace the personal topic or group with the source values. Quarkus Dev
Services, usage analytics, and the unused telemetry exporters are disabled
locally.

Click **New Fighters**, **New Location**, then **Fight!**. Repeat until each
team has won. Hero wins should appear only on the local statistics page;
villain wins should appear only on the cluster page. For a controlled run,
send the fight API a hero with level 100000 and villain with level 1, then swap
the levels. Its response includes `winnerTeam`; check that field for each
request. Use distinct names so you can track exact winners in the dashboards.
For example, run this at the repository root while the API forward is active:

```sh
python3 - <<'PY'
import json
import urllib.request
from uuid import uuid4

for team in ('heroes', 'villains'):
    name = f'Demo-{team}-{uuid4().hex[:6]}'
    hero_wins = team == 'heroes'
    body = {
        'hero': {'name': name if hero_wins else 'Demo Hero',
                 'level': 100000 if hero_wins else 1,
                 'powers': 'Demo',
                 'picture': 'https://example.invalid/hero.png'},
        'villain': {'name': 'Demo Villain' if hero_wins else name,
                    'level': 1 if hero_wins else 100000,
                    'powers': 'Demo',
                    'picture': 'https://example.invalid/villain.png'},
        'location': {'name': 'Demo Arena', 'description': 'Kafka demo',
                     'picture': 'https://example.invalid/arena.png'},
    }
    request = urllib.request.Request(
        'http://localhost:8082/api/fights',
        data=json.dumps(body).encode(),
        headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(request) as response:
        fight = json.load(response)
    assert fight['winnerTeam'] == team, fight
    print(name, fight['winnerTeam'], fight['id'])
PY
```

Edit `event-statistics/src/main/resources/META-INF/resources/index.html` in
the upstream checkout while dev mode is running. For example, change the
statistics title. Reload the local page and make another hero win. The cluster
page keeps its original title.

## Finish and repeat

Stop the local Quarkus process, then run:

```sh
bash examples/kafka/cleanup-demo.sh
```

This leaves the intercept, waits for its route to close, disables the split,
waits for phase `Disabled`, and restores the saved producer image and statistics
consumer settings. It keeps the application and cluster. The saved state is
archived under `examples/kafka/.demo-state-<timestamp>/`; a second preparation
can run. Do not delete shadow topics manually. Consumed personal events are
not replayed; unconsumed personal records return to the application during
route cleanup. Restart the statistics port-forward and dashboard after Pod
replacement.

When the recording is finished, reverse the producer patch only if the
checkout source still matches it:

```sh
cd examples/kafka/quarkus-super-heroes
git apply --reverse --check ../patches/rest-fights-winner-team.patch
git apply --reverse ../patches/rest-fights-winner-team.patch
```

| Initial image | Digest |
| --- | --- |
| Kafka broker | `sha256:777f2dddec6970003f1f27922a8c317d87140567b0537e801d35669ad9a81faf` |
| Apicurio | `sha256:3ff121c9f744d535ef770b80ff95693bc95063295316a5864b56312b6edfb4e2` |
| Event statistics | `sha256:c9fcfe363a8d82e93f67b1a0950c7417492cc56c43855acd90dbc55cb151bcd1` |
| Original fight producer | `sha256:8f2cf2abb3d49bbb393c76943bb154d8de2c4028470b026897c7919f003b38cc` |

The local images built for this rehearsal have these Docker image IDs. They
identify the exact local builds; they are not registry digests.

| Local image | Image ID |
| --- | --- |
| Patched fight producer | `sha256:9e2885513ab2304656488d9d0238813da399461ff1e5189804bf4257bdc7d138` |
| Telepresence tel2, demo.1 | `sha256:511dd39c6c8517f01e4233b84cb215e5003bd2a9f40c4978c585fd3fa69ff6e2` |
| Kafka provider, demo.1 | `sha256:2284780c147bf36b65fdb7d612f0137bed243879d64e6aefc068c3bb122f7b80` |

Use a new `TELEPRESENCE_VERSION` and matching `--version` image tag when
rebuilding Telepresence code for a later demo revision. A new tag makes Helm
roll out the new images instead of reusing a cached build.

## Verified rehearsal

On the `superheroes` kind cluster, the original dashboard consumed fights in
the `event-statistics` group. After preparation, two separate runs confirmed
that named hero wins reached only the local statistics consumer and named
villain wins reached only the cluster consumer. The local consumer decoded
the Avro events through Apicurio. An edit to the local statistics page appeared
in Quarkus dev mode without changing the cluster page. Cleanup closed the
route, disabled the split, and restored the original producer image and
statistics environment. The second preparation and routing run then passed.
