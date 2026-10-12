// Package config reads the environment once at startup. Nothing else reads os.Getenv.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"nibwire/internal/core/model"
)

const (
	MinSessionSecretLength = 32
	DefaultMaxSendsPerDay  = 50_000

	// DefaultSendConcurrency is how many sends are in flight at once. It is a starting point, not a
	// number that is right for every account: what a deployment needs is the account's rate times how
	// long one send takes, so an account allowed 450 a second against 80 ms of latency needs at least
	// 36. `nibwire quota` prints what the current pair comes to (docs/ses-smoke.md).
	DefaultSendConcurrency = 16
	// MaxSendConcurrency is where a typo stops being a typo: below this, more workers than there is
	// memory for one send each is the only cost.
	MaxSendConcurrency = 256
	// DefaultSendHeadroom is the fraction of the account's rate to aim at (D17).
	DefaultSendHeadroom = 0.9
	// MaxSendRateCap bounds SEND_RATE_PER_SECOND. It replaced a hard 14: a deployment was never really
	// limited to fourteen a second, and saying so only made the biggest accounts slow.
	MaxSendRateCap = 5000
)

type Config struct {
	SessionSecret string
	AppURL        string // no trailing slash
	DataDir       string
	DBPath        string
	SendingOn     bool
	SendApproval  string // "auto" or "manual"
	MCPHTTPAddr   string
	// SendConcurrency is SEND_CONCURRENCY: how many sends may be in flight at once (D16).
	SendConcurrency int
	SESRegion       string
	SESConfigSet    string
	// SESEndpoint replaces the SES service address (SES_ENDPOINT). The compose smoke stack points it
	// at a fake SES; empty means the real one for the region.
	SESEndpoint string
	// SESAccessKeyID and SESSecretAccessKey are the credentials this project's documentation names
	// (SES_ACCESS_KEY_ID / SES_SECRET_ACCESS_KEY). Empty means the SDK's own chain — an instance or
	// task role — which is the better answer on AWS.
	SESAccessKeyID     string
	SESSecretAccessKey string
	SESTopicARN        string
	ArchiveHosts       string
	// TrustedProxies are the networks whose X-Forwarded-For may be believed (TRUSTED_PROXIES).
	// Empty means none: the peer address is used, which is the safe default and the right one for a
	// process serving traffic directly.
	TrustedProxies []string
	AlertFrom      string
	AdminEmail     string
	// FakeSESLog, when set, makes the CLI and server record sends to that file instead of calling
	// SES. It is how the end-to-end test observes a send; phase 5's compose stack uses a real fake
	// endpoint instead.
	FakeSESLog string
	// FakeSESDomains is the stand-in identity list used alongside FakeSESLog: the domains the
	// pre-send check and `nibwire domains` see. Production reads SES.
	FakeSESDomains []string
	Limits         model.Limits
}

// LoadFile reads a KEY=VALUE file, which is how `--config` works: the file is the deployment's
// settings and the environment still wins, so a one-off `MAX_SENDS_PER_DAY=10 nibwire campaign
// send` overrides it without editing it. Within the file, the last mention of a key wins.
//
// The format is the one every container stack already uses: one KEY=VALUE per line, blank lines and
// #-comments ignored, an optional "export " prefix, and single or double quotes around a value that
// has spaces. A line that is neither gets the file's name and the line number in the error, because
// a settings file that silently ignores a line is how a deployment ends up missing its secret.
func LoadFile(path string, base map[string]string) (map[string]string, error) {
	if base == nil {
		base = environ()
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: the operator names the file on the command line
	if err != nil {
		return nil, err
	}
	fromFile := make(map[string]string, 16)
	for i, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: %q is not KEY=VALUE", path, i+1, raw)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: the key is empty", path, i+1)
		}
		fromFile[key] = unquote(strings.TrimSpace(value))
	}
	// The environment is applied last, so it wins: a one-line override on the command line is worth
	// more than editing the deployment's file, and that is the precedence an operator expects.
	for key, value := range base {
		fromFile[key] = value
	}
	return fromFile, nil
}

// unquote removes one layer of matching quotes from a value.
func unquote(value string) string {
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
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
	// Sending on, but nowhere to send: the default path used to build a real SES client and let the
	// first send fail on missing credentials, which reads as a bug in the sender rather than as a
	// deployment that was never finished. SENDING_ENABLED is false by default, so a fresh deployment
	// still starts; it is only the half-configured one that is refused, and it is refused at startup.
	if env["SENDING_ENABLED"] == "true" &&
		env["SES_REGION"] == "" && env["SES_ENDPOINT"] == "" && env["NIBWIRE_FAKE_SES_LOG"] == "" {
		problems = append(problems, "SENDING_ENABLED=true, but there is no SES to send through: set "+
			"SES_REGION (and SES_ACCESS_KEY_ID/SES_SECRET_ACCESS_KEY, or an instance role), or point "+
			"SES_ENDPOINT at a local fake SES, or set NIBWIRE_FAKE_SES_LOG to write sends to a file.")
	}
	if len(problems) > 0 {
		return Config{}, errors.New("nibwire can't start. " + strings.Join(problems, " "))
	}
	dataDir := or(env["NIBWIRE_DATA_DIR"], "/data")
	return Config{
		SessionSecret: env["SESSION_SECRET"],
		AppURL:        strings.TrimSuffix(or(env["APP_URL"], "http://localhost:8080"), "/"),
		DataDir:       dataDir,
		DBPath:        or(env["NIBWIRE_DB"], dataDir+"/nibwire.db"),
		SendingOn:     env["SENDING_ENABLED"] == "true",
		SendApproval:  approval,
		MCPHTTPAddr:   or(env["MCP_HTTP_ADDR"], "127.0.0.1:8081"),
		SendConcurrency: intIn(env["SEND_CONCURRENCY"], 1, MaxSendConcurrency,
			DefaultSendConcurrency),
		SESRegion:          env["SES_REGION"],
		SESConfigSet:       env["SES_CONFIGURATION_SET"],
		SESEndpoint:        env["SES_ENDPOINT"],
		SESAccessKeyID:     env["SES_ACCESS_KEY_ID"],
		SESSecretAccessKey: env["SES_SECRET_ACCESS_KEY"],
		SESTopicARN:        env["SES_SNS_TOPIC_ARN"],
		ArchiveHosts:       env["ARCHIVE_HOSTS"],
		TrustedProxies:     splitList(env["TRUSTED_PROXIES"]),
		AlertFrom:          env["ALERT_FROM"],
		AdminEmail:         env["ADMIN_EMAIL"],
		FakeSESLog:         env["NIBWIRE_FAKE_SES_LOG"],
		FakeSESDomains:     splitList(env["NIBWIRE_FAKE_SES_DOMAINS"]),
		Limits: model.Limits{
			BounceRate:    0.02,
			ComplaintRate: 0.0008,
			MaxEmailBytes: 100_000,
			// 0 is "no ceiling of our own": the rate follows the account (D17). It used to default to
			// 10 and cap at 14, which is a guess about an account the deployment can see.
			SendRatePerSecond:        intIn(env["SEND_RATE_PER_SECOND"], 1, MaxSendRateCap, 0),
			SendRateHeadroom:         floatIn(env["SEND_RATE_HEADROOM"], 0.1, 1, DefaultSendHeadroom),
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

// splitList reads a comma-separated list: blanks are dropped, so "a,,b," is two entries.
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
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

// floatIn is intIn for a fraction, with the same convention: anything out of range falls back rather
// than failing the process, because a badly spelled setting should not be a deployment that will not
// start.
func floatIn(v string, lo, hi, fallback float64) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < lo || f > hi {
		return fallback
	}
	return f
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
