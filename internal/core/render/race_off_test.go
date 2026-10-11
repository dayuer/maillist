//go:build !race

package render

// raceEnabled marks a race-instrumented build; see slow_test.go.
const raceEnabled = false
