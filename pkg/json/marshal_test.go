package json

import (
	"testing"
	"time"
)

func TestDurationString(t *testing.T) {
	want := struct {
		Duration time.Duration `json:"duration"`
	}{Duration: 4 * time.Second}
	data, err := Marshal(&want)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"duration":"4s"}` {
		t.Fatalf("Marshal() = %s", got)
	}

	var got struct {
		Duration time.Duration `json:"duration"`
	}
	if err = Unmarshal(data, &got, true); err != nil {
		t.Fatal(err)
	}
	if got.Duration != want.Duration {
		t.Fatalf("duration = %s, want %s", got.Duration, want.Duration)
	}
}

func TestDurationRejectsNumber(t *testing.T) {
	var got struct {
		Duration time.Duration `json:"duration"`
	}
	if err := Unmarshal([]byte(`{"duration":4000000000}`), &got, true); err == nil {
		t.Fatal("Unmarshal() accepted a numeric duration")
	}
}
