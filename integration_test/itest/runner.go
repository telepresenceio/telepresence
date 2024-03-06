package itest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/dtest"
)

type Runner interface {
	AddClusterSuite(func(context.Context) TestingSuite)
	AddNamespacePairSuite(suffix string, f func(NamespacePair) TestingSuite)
	AddTrafficManagerSuite(suffix string, f func(NamespacePair) TestingSuite)
	AddConnectedSuite(suffix string, f func(NamespacePair) TestingSuite)
	AddMultipleServicesSuite(suffix, name string, f func(MultipleServices) TestingSuite)
	AddSingleServiceSuite(suffix, name string, f func(SingleService) TestingSuite)
	RunTests(context.Context)
}

type namedRunner struct {
	withMultipleServices []func(MultipleServices) TestingSuite
	withSingleService    []func(SingleService) TestingSuite
}

type suffixedRunner struct {
	withNamespace      []func(NamespacePair) TestingSuite
	withTrafficManager []func(NamespacePair) TestingSuite
	withConnected      []func(NamespacePair) TestingSuite
	withName           map[string]*namedRunner
}

type runner struct {
	withCluster []func(ctx context.Context) TestingSuite
	withSuffix  map[string]*suffixedRunner
}

var defaultRunner Runner = &runner{withSuffix: make(map[string]*suffixedRunner)} //nolint:gochecknoglobals // integration test config

// AddClusterSuite adds a constructor for a test suite that requires a cluster to run to the default runner.
func AddClusterSuite(f func(context.Context) TestingSuite) {
	defaultRunner.AddClusterSuite(f)
}

// AddClusterSuite adds a constructor for a test suite that requires a cluster to run.
func (r *runner) AddClusterSuite(f func(context.Context) TestingSuite) {
	r.withCluster = append(r.withCluster, f)
}

func (r *runner) forSuffix(suffix string) *suffixedRunner {
	sr, ok := r.withSuffix[suffix]
	if !ok {
		sr = &suffixedRunner{withName: map[string]*namedRunner{}}
		r.withSuffix[suffix] = sr
	}
	return sr
}

// AddNamespacePairSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized to the default runner.
func AddNamespacePairSuite(suffix string, f func(NamespacePair) TestingSuite) {
	defaultRunner.AddNamespacePairSuite(suffix, f)
}

// AddNamespacePairSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized.
func (r *runner) AddNamespacePairSuite(suffix string, f func(NamespacePair) TestingSuite) {
	sr := r.forSuffix(suffix)
	sr.withNamespace = append(sr.withNamespace, f)
}

// AddTrafficManagerSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized and a traffic manager is installed.
func AddTrafficManagerSuite(suffix string, f func(NamespacePair) TestingSuite) {
	defaultRunner.AddTrafficManagerSuite(suffix, f)
}

// AddTrafficManagerSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized and a traffic manager is installed.
func (r *runner) AddTrafficManagerSuite(suffix string, f func(NamespacePair) TestingSuite) {
	sr := r.forSuffix(suffix)
	sr.withTrafficManager = append(sr.withTrafficManager, f)
}

func (r *suffixedRunner) forName(name string) *namedRunner {
	nr, ok := r.withName[name]
	if !ok {
		nr = &namedRunner{}
		r.withName[name] = nr
	}
	return nr
}

// AddConnectedSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, and telepresence is connected.
func AddConnectedSuite(suffix string, f func(NamespacePair) TestingSuite) {
	defaultRunner.AddConnectedSuite(suffix, f)
}

// AddConnectedSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, and telepresence is connected.
func (r *runner) AddConnectedSuite(suffix string, f func(NamespacePair) TestingSuite) {
	sr := r.forSuffix(suffix)
	sr.withConnected = append(sr.withConnected, f)
}

// AddMultipleServicesSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, multiple services has been installed, and telepresence is connected.
func AddMultipleServicesSuite(suffix, name string, f func(services MultipleServices) TestingSuite) {
	defaultRunner.AddMultipleServicesSuite(suffix, name, f)
}

// AddMultipleServicesSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized, multiple services has been installed, and telepresence is connected.
func (r *runner) AddMultipleServicesSuite(suffix, name string, f func(services MultipleServices) TestingSuite) {
	nr := r.forSuffix(suffix).forName(name)
	nr.withMultipleServices = append(nr.withMultipleServices, f)
}

// AddSingleServiceSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence is connected.
func AddSingleServiceSuite(suffix, name string, f func(services SingleService) TestingSuite) {
	defaultRunner.AddSingleServiceSuite(suffix, name, f)
}

// AddSingleServiceSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence is connected.
func (r *runner) AddSingleServiceSuite(suffix, name string, f func(services SingleService) TestingSuite) {
	nr := r.forSuffix(suffix).forName(name)
	nr.withSingleService = append(nr.withSingleService, f)
}

func RunTests(c context.Context) {
	defaultRunner.RunTests(c)
}

// RunTests creates all suites using the added constructors and runs them.
func (r *runner) RunTests(c context.Context) { //nolint:gocognit
	c = LoadEnv(c)
	dtest.WithMachineLock(c, func(c context.Context) {
		WithCluster(c, func(c context.Context) {
			func() {
				t := getT(c)
				for _, f := range r.withCluster {
					s := f(c)
					if suiteEnabled(c, s) {
						t.Run(s.SuiteName(), func(t *testing.T) {
							ts := f(c)
							ts.setContext(ts.AmendSuiteContext(c))
							suite.Run(t, ts)
						})
					}
				}
			}()
			for s, sr := range r.withSuffix {
				WithNamespacePair(c, GetGlobalHarness(c).Suffix()+s, func(np NamespacePair) {
					for _, f := range sr.withNamespace {
						np.RunSuite(f(np))
					}
					if len(sr.withTrafficManager)+len(sr.withConnected)+len(sr.withName) > 0 {
						WithTrafficManager(np, func(c context.Context, cnp NamespacePair) {
							for _, f := range sr.withTrafficManager {
								cnp.RunSuite(f(cnp))
							}
							if len(sr.withConnected)+len(sr.withName) > 0 {
								WithConnected(np, func(c context.Context, cnp NamespacePair) {
									for _, f := range sr.withConnected {
										cnp.RunSuite(f(cnp))
									}
									for n, nr := range sr.withName {
										if len(nr.withMultipleServices) > 0 {
											WithMultipleServices(cnp, n, 3, func(ms MultipleServices) {
												for _, f := range nr.withMultipleServices {
													ms.RunSuite(f(ms))
												}
											})
										}
										if len(nr.withSingleService) > 0 {
											WithSingleService(cnp, n, func(ss SingleService) {
												for _, f := range nr.withSingleService {
													ss.RunSuite(f(ss))
												}
											})
										}
									}
								})
							}
						})
					}
				})
			}
		})
	})
}
