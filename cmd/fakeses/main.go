// Command fakeses serves internal/fakessesrv: a fake Amazon SES for a local compose stack, so the
// app's real SES client can send without AWS.
//
//	docker compose -f deploy/compose.yaml -f deploy/compose.local.yaml up -d
//
// The routes and the behaviour are documented on the package.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"nibwire/internal/fakessesrv"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	flag.Parse()

	// FAKE_SES_DOMAINS is the sending-domain list the app's pre-send check reads, so a local stack
	// exercises the same domain check a deployment does.
	var options []fakessesrv.Option
	if domains := splitList(os.Getenv("FAKE_SES_DOMAINS")); len(domains) > 0 {
		options = append(options, fakessesrv.WithVerifiedDomains(domains...))
	}
	// The account's numbers, which is what the app reads from GetAccount and paces itself by. Without
	// them the app is told it has no quota and sends one a second, which is the right answer to a real
	// account that cannot be reached and the wrong one for a smoke test.
	if quota, ok := quotaFromEnv(); ok {
		options = append(options, fakessesrv.WithQuota(quota))
	}
	// And what the fake actually lets through, which is deliberately separate: a real account reports a
	// ceiling and enforces whatever it enforces.
	if rate := floatFromEnv("FAKE_SES_ENFORCED_RATE"); rate > 0 {
		options = append(options, fakessesrv.WithEnforcedRate(rate))
	}
	if quota := intFromEnv("FAKE_SES_DAILY_QUOTA"); quota > 0 {
		options = append(options, fakessesrv.WithDailyQuota(quota))
	}
	if latency := intFromEnv("FAKE_SES_LATENCY_MS"); latency > 0 {
		options = append(options, fakessesrv.WithLatency(
			time.Duration(latency)*time.Millisecond, time.Duration(latency/10)*time.Millisecond))
	}

	log.Printf("fakeses listening on %s", *addr)
	server := &http.Server{
		Addr:              *addr,
		Handler:           fakessesrv.New(nil, options...).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// quotaFromEnv reads FAKE_SES_MAX_SEND_RATE / FAKE_SES_MAX_24_HOUR_SEND / FAKE_SES_SENT_LAST_24_HOURS.
func quotaFromEnv() (fakessesrv.Quota, bool) {
	rate := floatFromEnv("FAKE_SES_MAX_SEND_RATE")
	if rate <= 0 {
		return fakessesrv.Quota{}, false
	}
	max24 := floatFromEnv("FAKE_SES_MAX_24_HOUR_SEND")
	if max24 == 0 {
		// -1 is what SES uses for an account without a 24-hour limit, and it is the safer default for
		// a smoke stack: a fake that ran out part way through would look like the app breaking.
		max24 = -1
	}
	return fakessesrv.Quota{
		MaxSendRate:     rate,
		Max24HourSend:   max24,
		SentLast24Hours: floatFromEnv("FAKE_SES_SENT_LAST_24_HOURS"),
	}, true
}

func floatFromEnv(name string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64)
	if err != nil {
		return 0
	}
	return value
}

func intFromEnv(name string) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return 0
	}
	return value
}

// splitList reads a comma-separated list, dropping blanks.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
