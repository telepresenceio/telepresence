package json

import (
	"log/slog"
	"time"

	"github.com/go-json-experiment/json"

	"github.com/telepresenceio/clog"
)

// Marshal serializes the given value to JSON deterministically and enforces unit serialization of durations.
func Marshal(value any) ([]byte, error) {
	opts := json.WithMarshalers(json.MarshalFunc(func(d time.Duration) ([]byte, error) {
		return json.Marshal(d.String())
	}))
	return json.Marshal(value, opts, json.Deterministic(true))
}

// Unmarshal deserializes the given JSON data into the given value. Durations are deserialized using the
// time.ParseDuration function (unit-aware).
func Unmarshal(data []byte, into any, rejectUnknown bool) error {
	opts := []json.Options{json.WithUnmarshalers(
		json.JoinUnmarshalers(
			json.UnmarshalFunc(func(b []byte, v *time.Duration) error {
				var s string
				err := json.Unmarshal(b, &s)
				if err != nil {
					return err
				}
				d, err := time.ParseDuration(s)
				if err == nil {
					*v = d
				}
				return err
			}),
			json.UnmarshalFunc(func(b []byte, v *slog.Level) error {
				var s string
				err := json.Unmarshal(b, &s)
				if err != nil {
					return err
				}
				d, err := clog.ParseLevel(s)
				if err == nil {
					*v = d
				}
				return err
			}),
		))}
	if rejectUnknown {
		opts = append(opts, json.RejectUnknownMembers(true))
	}
	return json.Unmarshal(data, into, opts...)
}
