package cli

import (
	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type Diag interface {
	diagName() string
	resultChan() chan DiagResult
	setup() error
	teardown() error
	run() error
	pass()
	fail(err error)
}

type BaseDiag struct {
	cmd *cobra.Command
	cc  connector.ConnectorClient
	mc  manager.ManagerClient
	ch  chan DiagResult
}

func (d *BaseDiag) run() error {
	return nil
}

func (d *BaseDiag) pass() {
}

func (d *BaseDiag) fail(err error) {
}

func (d *BaseDiag) setup() error {
	return nil
}

func (d *BaseDiag) teardown() error {
	return nil
}

func (d *BaseDiag) diagName() string {
	return "nil Diag"
}

func (d *BaseDiag) resultChan() chan DiagResult {
	return d.ch
}

type DiagResult interface {
	State() string
	Err() error
	DiagName() string
}

type BaseResult struct {
	diagName string
	state    string
	err      error
}

func (br *BaseResult) DiagName() string {
	return br.diagName
}

func (br *BaseResult) State() string {
	return br.state
}

func (br *BaseResult) Err() error {
	return br.err
}
