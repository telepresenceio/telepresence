package managers

import "testing"

// TestSpecWithKafka verifies WithKafka forces kafka.enabled=true and
// distinguishes the resulting spec's Key (and therefore Hash) from the
// spec it started from, so a manager installed with the provider is never
// adopted for, or by, a run without it.
func TestSpecWithKafka(t *testing.T) {
	base := Default
	overlaid := base.WithKafka()

	if !overlaid.Values.Kafka.Enabled {
		t.Fatalf("WithKafka: Values.Kafka.Enabled = false, want true")
	}
	if overlaid.Key == base.Key {
		t.Fatalf("WithKafka: Key unchanged (%q), want a distinguishing suffix", overlaid.Key)
	}
	if overlaid.Hash() == base.Hash() {
		t.Fatalf("WithKafka: Hash unchanged, want it to differ from the Kafka-disabled spec")
	}

	// Applying WithKafka to a different base spec must still hash
	// differently from overlaid: the overlay must not collapse distinct
	// specs onto the same release.
	other := NodeAgent().WithKafka()
	if other.Hash() == overlaid.Hash() {
		t.Fatalf("WithKafka: Default and NodeAgent overlays collide on Hash %q", other.Hash())
	}
}

// TestSpecWithKafkaIdempotent verifies WithKafka is a no-op on a spec that
// already enables Kafka, so re-applying it (e.g. the framework's
// RTEST_MANAGER_KAFKA overlay on top of the Kafka() catalog entry) never
// double-suffixes the Key or changes the Hash.
func TestSpecWithKafkaIdempotent(t *testing.T) {
	already := Kafka()
	overlaid := already.WithKafka()

	if overlaid.Key != already.Key {
		t.Fatalf("WithKafka: Key changed from %q to %q for an already-enabled spec", already.Key, overlaid.Key)
	}
	if overlaid.Hash() != already.Hash() {
		t.Fatalf("WithKafka: Hash changed for an already-enabled spec")
	}
}

// TestMergeKafka verifies Merge propagates an overlay's Kafka.Enabled onto
// the base values, the mechanism WithKafka relies on to reach the chart.
func TestMergeKafka(t *testing.T) {
	base := Values{}
	merged := Merge(base, Values{Kafka: KafkaValues{Enabled: true}})
	if !merged.Kafka.Enabled {
		t.Fatalf("Merge: Kafka.Enabled = false, want true")
	}
}
