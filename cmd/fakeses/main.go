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
	"strings"
	"time"

	"mailcheap/internal/fakessesrv"
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

	log.Printf("fakeses listening on %s", *addr)
	server := &http.Server{
		Addr:              *addr,
		Handler:           fakessesrv.New(nil, options...).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
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
