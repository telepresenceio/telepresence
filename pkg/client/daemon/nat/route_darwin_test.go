package nat

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/datawire/dlib/dexec"
)

type env struct {
	pfconf string
	before string
}

/*
 *  192.0.2.0/24 (TEST-NET-1),
 *  198.51.100.0/24 (TEST-NET-2)
 *  203.0.113.0/24 (TEST-NET-3)
 */

var environments = []env{
	{pfconf: ""},
	{pfconf: "set skip on lo\n"},
	{pfconf: "block return quick proto tcp from any to 192.0.2.0/24\n"},
	{pfconf: "anchor {\n    block return quick proto tcp from any to 192.0.2.0/24\n}\n"},
}

func (e *env) setup(c context.Context) error {
	o1, err := dexec.CommandContext(c, "pfctl", "-sr").CombinedOutput()
	if err != nil {
		return err
	}
	o2, err := dexec.CommandContext(c, "pfctl", "-sn").CombinedOutput()
	if err != nil {
		return err
	}
	output := string(o1) + string(o2)
	lines := strings.Split(output, "\n")

	for _, kw := range []string{"scrub-anchor", "nat-anchor", "rdr-anchor", "anchor"} {
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) > 0 && fields[0] == kw {
				e.before += line + "\n"
			}
		}
	}

	err = pf(c, []string{"-F", "all"}, "")
	if err != nil {
		return err
	}
	err = pf(c, []string{"-f", "/dev/stdin"}, e.pfconf)
	if err != nil {
		return err
	}
	return nil
}

func (e *env) teardown(c context.Context) error {
	_ = pf(c, []string{"-F", "all"}, "")
	return pf(c, []string{"-f", "/dev/stdin"}, e.before)
}

func TestSorted(t *testing.T) {
	g, _ := testGroup()
	g.Go("sorted-test", func(c context.Context) (err error) {
		tr := newRouter("test-table")
		if err = tr.Enable(c); err != nil {
			return err
		}
		defer func() {
			_ = tr.Disable(c)
		}()
		if err = tr.ForwardTCP(c, "192.0.2.1", "", "4321"); err != nil {
			return err
		}
		if err = tr.ForwardTCP(c, "192.0.2.3", "", "4323"); err != nil {
			return err
		}
		if err = tr.ForwardTCP(c, "192.0.2.2", "", "4322"); err != nil {
			return err
		}
		if err = tr.ForwardUDP(c, "192.0.2.4", "", "2134"); err != nil {
			return err
		}
		entries := tr.sorted()
		if !reflect.DeepEqual(entries, []Entry{
			{Address{"tcp", "192.0.2.1", ""}, "4321"},
			{Address{"tcp", "192.0.2.2", ""}, "4322"},
			{Address{"tcp", "192.0.2.3", ""}, "4323"},
			{Address{"udp", "192.0.2.4", ""}, "2134"},
		}) {
			return fmt.Errorf("not sorted: %s", entries)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
