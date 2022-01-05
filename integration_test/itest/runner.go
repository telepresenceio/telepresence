package itest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/dtest"
)

type Runner interface {
	AddClusterSuite(func(context.Context) suite.TestingSuite)
	AddNamespacePairSuite(suffix string, f func(NamespacePair) suite.TestingSuite)
	AddConnectedSuite(suffix string, f func(NamespacePair) suite.TestingSuite)
	AddHelmAndServiceSuite(suffix, name string, f func(HelmAndService) suite.TestingSuite)
	AddMultipleServicesSuite(suffix, name string, f func(MultipleServices) suite.TestingSuite)
	AddSingleServiceSuite(suffix, name string, f func(SingleService) suite.TestingSuite)
	RunTests(*testing.T)
}

type namedRunner struct {
	withMultipleServices []func(MultipleServices) suite.TestingSuite
	withSingleService    []func(SingleService) suite.TestingSuite
	withHelmAndService   []func(HelmAndService) suite.TestingSuite
}

type suffixedRunner struct {
	withNamespace  []func(NamespacePair) suite.TestingSuite
	withConnection []func(NamespacePair) suite.TestingSuite
	withName       map[string]*namedRunner
}

type runner struct {
	withCluster []func(ctx context.Context) suite.TestingSuite
	withSuffix  map[string]*suffixedRunner
}

var defaultRunner Runner = &runner{withSuffix: make(map[string]*suffixedRunner)}

// AddClusterSuite adds a constructor for a test suite that requires a cluster to run to the default runner.
func AddClusterSuite(f func(context.Context) suite.TestingSuite) {
	defaultRunner.AddClusterSuite(f)
}

// AddClusterSuite adds a constructor for a test suite that requires a cluster to run.
func (r *runner) AddClusterSuite(f func(context.Context) suite.TestingSuite) {
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
func AddNamespacePairSuite(suffix string, f func(NamespacePair) suite.TestingSuite) {
	defaultRunner.AddNamespacePairSuite(suffix, f)
}

// AddNamespacePairSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized.
func (r *runner) AddNamespacePairSuite(suffix string, f func(NamespacePair) suite.TestingSuite) {
	sr := r.forSuffix(suffix)
	sr.withNamespace = append(sr.withNamespace, f)
}

// AddConnectedSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized and telepresence is connected to the default runner.
func AddConnectedSuite(suffix string, f func(NamespacePair) suite.TestingSuite) {
	defaultRunner.AddConnectedSuite(suffix, f)
}

// AddConnectedSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized and telepresence is connected.
func (r *runner) AddConnectedSuite(suffix string, f func(NamespacePair) suite.TestingSuite) {
	sr := r.forSuffix(suffix)
	sr.withConnection = append(sr.withConnection, f)
}

func (r *suffixedRunner) forName(name string) *namedRunner {
	nr, ok := r.withName[name]
	if !ok {
		nr = &namedRunner{}
		r.withName[name] = nr
	}
	return nr
}

// AddMultipleServicesSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, multiple services has been installed, and telepresence is connected.
func AddMultipleServicesSuite(suffix, name string, f func(services MultipleServices) suite.TestingSuite) {
	defaultRunner.AddMultipleServicesSuite(suffix, name, f)
}

// AddMultipleServicesSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized, multiple services has been installed, and telepresence is connected.
func (r *runner) AddMultipleServicesSuite(suffix, name string, f func(services MultipleServices) suite.TestingSuite) {
	nr := r.forSuffix(suffix).forName(name)
	nr.withMultipleServices = append(nr.withMultipleServices, f)
}

// AddSingleServiceSuite adds a constructor for a test suite to the default runner that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence is connected.
func AddSingleServiceSuite(suffix, name string, f func(services SingleService) suite.TestingSuite) {
	defaultRunner.AddSingleServiceSuite(suffix, name, f)
}

// AddSingleServiceSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence is connected.
func (r *runner) AddSingleServiceSuite(suffix, name string, f func(services SingleService) suite.TestingSuite) {
	nr := r.forSuffix(suffix).forName(name)
	nr.withSingleService = append(nr.withSingleService, f)
}

// AddHelmAndServiceSuite adds a constructor for a TestingSuite to the default runner that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence has been installed using Helm.
func AddHelmAndServiceSuite(suffix, name string, f func(services HelmAndService) suite.TestingSuite) {
	defaultRunner.AddHelmAndServiceSuite(suffix, name, f)
}

// AddHelmAndServiceSuite adds a constructor for a test suite that requires a cluster where a namespace
// pair has been initialized, a service has been installed, and telepresence has been installed using Helm.
func (r *runner) AddHelmAndServiceSuite(suffix, name string, f func(services HelmAndService) suite.TestingSuite) {
	nr := r.forSuffix(suffix).forName(name)
	nr.withHelmAndService = append(nr.withHelmAndService, f)
}

func RunTests(t *testing.T) {
	defaultRunner.RunTests(t)
}

// RunTests creates all suites using the added constructors and runs them
func (r *runner) RunTests(t *testing.T) { //nolint:gocognit
	c := TestContext(t)
	dtest.WithMachineLock(c, func(c context.Context) {
		WithCluster(c, func(c context.Context) {
			for _, f := range r.withCluster {
				suite.Run(t, f(c))
			}
			for s, sr := range r.withSuffix {
				WithNamespacePair(c, GetGlobalHarness(c).Suffix()+s, func(np NamespacePair) {
					for _, f := range sr.withNamespace {
						np.RunSuite(f(np))
					}
					if len(sr.withConnection)+len(sr.withName) > 0 {
						WithConnection(np, func(c context.Context, cnp NamespacePair) {
							for _, f := range sr.withConnection {
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
					for n, nr := range sr.withName {
						if len(nr.withHelmAndService) > 0 {
							WithSingleService(np, n, func(ss SingleService) {
								if len(nr.withHelmAndService) > 0 {
									WithHelmAndService(ss, func(hs HelmAndService) {
										for _, f := range nr.withHelmAndService {
											ss.RunSuite(f(hs))
										}
									})
								}
							})
						}
					}
				})
			}
		})
	})
}
