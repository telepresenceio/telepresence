// Package enginetest is the shared conformance suite for engine.Engine
// implementations. RunConformance exercises the lifecycle scenarios in the
// queue-splitting plan's provider engine contract once, against a Harness
// supplied by each provider's own test glue.
//
// The suite runs against real brokers, never a fake or mock: the contract it
// verifies -- fencing, crash recovery, and proved handback -- depends on
// broker-native guarantees a fake cannot reproduce. Each provider package
// (Kafka, RabbitMQ, ...) has its own *_test.go file that builds a Harness
// wired to a real broker reachable from the test environment and calls
// RunConformance from a normal Go test function. A broker that is not
// reachable is a skip, reported through Harness.Skip, not a failure.
package enginetest
