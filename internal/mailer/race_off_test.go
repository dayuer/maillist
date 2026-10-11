//go:build !race

package mailer

// raceEnabled marks a race-instrumented build; see race_on_test.go.
const raceEnabled = false
