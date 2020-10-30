package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/datawire/ambassador/pkg/supervisor"
)

func Test_findTrafficManager(t *testing.T) {
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "find-traffic-manager",
		Work: func(p *supervisor.Process) error {
			got, err := findTrafficManager(p)
			if err != nil {
				t.Error(err)
				return err
			}
			s, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Error(err)
				return err
			}
			t.Log(string(s))
			return nil
		},
	})
	sup.Run()
}

func Test_ensureTrafficManager(t *testing.T) {
	sup := supervisor.WithContext(context.Background())
	sup.Supervise(&supervisor.Worker{
		Name: "ensure-traffic-manager",
		Work: func(p *supervisor.Process) error {
			sshd, api, err := ensureTrafficManager(p)
			if err != nil {
				t.Error(err)
				return err
			}
			if sshd != 8022 {
				t.Error("expected sshd port to be 8082")
			}
			if api != 8081 {
				t.Error("expected api port to be 8081")
			}
			return nil
		},
	})
	sup.Run()
}
