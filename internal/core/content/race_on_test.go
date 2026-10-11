//go:build race

package content

// raceEnabled marks a race-instrumented build. The slow-input budgets in slow_test.go are
// production numbers, and the detector's overhead makes them meaningless here, so those tests
// measure with a scaled budget and log the real number either way.
const raceEnabled = true
