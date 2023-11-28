package integration_test

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *notConnectedSuite) getGlobalSearchList() []string {
	const (
		tcpParamKey   = `System\CurrentControlSet\Services\Tcpip\Parameters`
		searchListKey = `SearchList`
	)
	rk, err := registry.OpenKey(registry.LOCAL_MACHINE, tcpParamKey, registry.QUERY_VALUE)
	if os.IsNotExist(err) {
		err = nil
	}
	s.Require().NoError(err)
	defer rk.Close()
	csv, _, err := rk.GetStringValue(searchListKey)
	if os.IsNotExist(err) {
		err = nil
	}
	s.Require().NoError(err)
	return strings.Split(csv, ",")
}

func (s *notConnectedSuite) Test_DNSSearchRestored() {
	beforeConnect := s.getGlobalSearchList()
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	afterConnect := s.getGlobalSearchList()
	s.Assert().NotEqual(beforeConnect, afterConnect)
	itest.TelepresenceQuitOk(ctx)
	afterQuit := s.getGlobalSearchList()
	s.Assert().Equal(beforeConnect, afterQuit)
}
