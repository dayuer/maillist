//go:build !race

package archive

// raceEnabled marks a race-instrumented build. The slow-input budgets in slow_test.go are production
// numbers, and the detector's overhead makes them meaningless here, so those tests measure with a
// scaled budget and log the real number either way (the pattern phase 4 settled on).
const raceEnabled = false
