//go:build race

package mailer

// raceEnabled marks a race-instrumented build. The 50k-insert budget in engine_bench_test.go is a
// production number, and the detector's overhead makes it meaningless here, so the test reports its
// measurement and skips the assertion instead of failing on instrumentation cost.
const raceEnabled = true
