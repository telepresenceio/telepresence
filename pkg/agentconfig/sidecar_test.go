package agentconfig

import "testing"

func TestAgentGIDFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("ok = true, want false")
		}
		if gid != 0 {
			t.Fatalf("gid = %d, want 0", gid)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "")
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("ok = true, want false")
		}
		if gid != 0 {
			t.Fatalf("gid = %d, want 0", gid)
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "7439")
		gid, ok, err := AgentGIDFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("ok = false, want true")
		}
		if gid != 7439 {
			t.Fatalf("gid = %d, want 7439", gid)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Setenv(EnvAgentGID, "not-a-number")
		_, _, err := AgentGIDFromEnv()
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
	})
}
