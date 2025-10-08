package cmd

import (
	"testing"

	"github.com/njayp/ophis/test"
)

func TestMCP(t *testing.T) {
	ctx := WithSubCommands(t.Context())
	cmd := Telepresence(ctx, nil)
	tools := test.GetTools(t, cmd)
	test.ToolNames(t, tools,
		"telepresence_quit",
		"telepresence_status",
		"telepresence_connect",
		"telepresence_intercept",
		"telepresence_ingest",
		"telepresence_leave",
		"telepresence_list",
	)
}
