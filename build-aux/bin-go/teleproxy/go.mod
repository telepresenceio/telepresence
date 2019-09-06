module github.com/datawire/build-aux/bin-go/teleproxy

go 1.12

require github.com/datawire/teleproxy v0.3.16

// Fix invalid pseudo-version that Go 1.13 complains about.
replace github.com/census-instrumentation/opencensus-proto v0.1.0-0.20181214143942-ba49f56771b8 => github.com/census-instrumentation/opencensus-proto v0.0.3-0.20181214143942-ba49f56771b8
