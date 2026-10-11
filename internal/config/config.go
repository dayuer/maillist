// Package config reads the environment once at startup. Nothing else reads os.Getenv.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"mailcheap/internal/core/model"
)

const (
	MinSessionSecretLength = 32
	DefaultSendRate        = 10
	MaxSendRate            = 14
	DefaultMaxSendsPerDay  = 50_000
)

type Config struct {
	SessionSecret string
	AppURL        string // no trailing slash
	DataDir       string
	DBPath        string
	SendingOn     bool
	SendApproval  string // "auto" or "manual"
	MCPHTTPAddr   string
	SESRegion     string
	SESConfigSet  string
	SESTopicARN   string
	ArchiveHosts  string
	AlertFrom     string
	AdminEmail    string
	Limits        model.Limits
}

// Load reads env (os.Environ when nil) and returns every problem at once.
func Load(env map[string]string) (Config, error) {
	if env == nil {
		env = environ()
	}
	var problems []string
	if len(env["SESSION_SECRET"]) < MinSessionSecretLength {
		problems = append(problems, fmt.Sprintf(
			"SESSION_SECRET must be at least %d characters (generate one with: openssl rand -base64 48).", MinSessionSecretLength))
	}
	approval := or(env["SEND_APPROVAL"], "auto")
	if approval != "auto" && approval != "manual" {
		problems = append(problems, `SEND_APPROVAL must be "auto" or "manual".`)
	}
	if len(problems) > 0 {
		return Config{}, errors.New("mailcheap can't start. " + strings.Join(problems, " "))
	}
	dataDir := or(env["MAILCHEAP_DATA_DIR"], "/data")
	return Config{
		SessionSecret: env["SESSION_SECRET"],
		AppURL:        strings.TrimSuffix(or(env["APP_URL"], "http://localhost:8080"), "/"),
		DataDir:       dataDir,
		DBPath:        or(env["MAILCHEAP_DB"], dataDir+"/mailcheap.db"),
		SendingOn:     env["SENDING_ENABLED"] == "true",
		SendApproval:  approval,
		MCPHTTPAddr:   or(env["MCP_HTTP_ADDR"], "127.0.0.1:8081"),
		SESRegion:     env["SES_REGION"],
		SESConfigSet:  env["SES_CONFIGURATION_SET"],
		SESTopicARN:   env["SES_SNS_TOPIC_ARN"],
		ArchiveHosts:  env["ARCHIVE_HOSTS"],
		AlertFrom:     env["ALERT_FROM"],
		AdminEmail:    env["ADMIN_EMAIL"],
		Limits: model.Limits{
			BounceRate:               0.02,
			ComplaintRate:            0.0008,
			MaxEmailBytes:            100_000,
			SendRatePerSecond:        intIn(env["SEND_RATE_PER_SECOND"], 1, MaxSendRate, DefaultSendRate),
			WelcomeEmailsPer10Minute: 100,
			MaxSendsPerDay:           intIn(env["MAX_SENDS_PER_DAY"], 1, 10_000_000, DefaultMaxSendsPerDay),
		},
	}, nil
}

func or(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// intIn parses v, falling back when it isn't an integer in [lo, hi]. Stricter than upstream's
// Number.parseInt: "5.5" and " 7 " are rejected rather than truncated/trimmed (see the test above).
func intIn(v string, lo, hi, fallback int) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fallback
	}
	return n
}

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}
