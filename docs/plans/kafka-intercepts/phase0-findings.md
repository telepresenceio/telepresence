# Kafka transactional feasibility findings

Date: 2026-08-22. Verdict: **GO**.

The maintained tests in `pkg/kafkaintercept` passed against real single-node
KRaft brokers on both supported conformance lines:

- Apache Kafka 3.8.0, image digest
  `sha256:c89f315cff967322c5d2021434b32271393cb193aa7ec1d43e97341924e57069`;
- Apache Kafka 4.3.1, image digest
  `sha256:77e3df9054047a88b520d0cc46e16696d3b22022e1d580aeccd2632df6532837`;
- franz-go 1.21.6 and kadm 1.18.0.

`TestTransactionalSplitterConformance` consumed two source partitions through
the application's original group. In bounded transactions it routed records by
Kafka header to application and personal shadows, preserved the source
partition and record metadata, and committed the original group offsets. Once
the group reached the source ends, every source record appeared exactly once
across the two destinations to a `read_committed` consumer.

`TestTransactionalSplitterRecoversOpenTransaction` produced a shadow record in
an open transaction and closed the producer without committing, modeling a
process loss after the broker accepted the publish. A replacement splitter
initialized the same transactional ID, recovered the source record from the
unchanged original-group offset, and committed it atomically. A
`read_committed` consumer observed exactly one destination record; the stale
open transaction was not exposed.

These results establish the design's central invariant: the original consumer
group advances only in the transaction that makes one destination copy
visible. They also confirm that the same implementation and client behavior
works across the Kafka 3.8 and current 4.x protocol lines.
