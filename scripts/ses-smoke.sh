#!/bin/sh
# Real-SES smoke test: send one issue to one verified address and watch the whole loop — delivery,
# the open pixel, a click, the one-click unsubscribe — come back through SNS.
#
# IT HAS NOT BEEN RUN. There are no AWS credentials in this repository and the phase it belongs to
# was executed without touching AWS, so this script is delivered untested against the real service
# (see docs/ses-smoke.md). Everything it does has been exercised against the fake SES of
# scripts/compose-smoke.sh, which is the closest thing to this that runs without an account.
#
# What it needs is in docs/ses-smoke.md. The short version:
#   - an SES sending domain, verified, with DKIM and MAIL FROM configured;
#   - an IAM user or role with ses:SendEmail and ses:GetEmailIdentity;
#   - a configuration set whose SNS topic is subscribed to <APP_URL>/api/webhooks/ses;
#   - a mailbox you can open (TEST_RECIPIENT);
#   - the deployment reachable at APP_URL over HTTPS, because SNS has to reach the webhook.
#
#   SES_REGION=… SES_ACCESS_KEY_ID=… SES_SECRET_ACCESS_KEY=… \
#   SES_CONFIGURATION_SET=… SES_SNS_TOPIC_ARN=… APP_URL=https://news.example.com \
#   TEST_RECIPIENT=you@example.com scripts/ses-smoke.sh
#
#   DRY_RUN=1 … scripts/ses-smoke.sh      print every command instead of running it
#
# It answers questions by asking you to look: whether the mail arrived is yours to see, and what the
# reporting shows afterwards is compared and printed. Nothing here deletes anything.
set -eu

DRY_RUN="${DRY_RUN:-0}"
SEND_WAIT="${SEND_WAIT:-15}"
SNS_WAIT="${SNS_WAIT:-180}"
SLUG="${SMOKE_SLUG:-ses-smoke}"
SUBJECT="${SMOKE_SUBJECT:-SES smoke test}"

say() { printf '\n=== %s\n' "$1"; }
pass() { printf 'ok    %s\n' "$1"; }
warn() { printf 'note  %s\n' "$1"; }
fail() { printf '\nFAIL: %s\n' "$1" >&2; exit 1; }
ask() { printf '\n>>> %s\n' "$1"; }

# run <args…> — the CLI, or the command it would run.
run() {
	if [ "$DRY_RUN" = "1" ]; then
		printf 'mailcheap %s\n' "$*"
		return 0
	fi
	mailcheap "$@"
}

step_ok() {
	if [ "$DRY_RUN" = "1" ]; then
		warn "not run (DRY_RUN=1)"
		return 1
	fi
	return 0
}

say "checking what this needs before it changes anything"
missing=""
for name in SES_REGION SES_ACCESS_KEY_ID SES_SECRET_ACCESS_KEY SES_CONFIGURATION_SET SES_SNS_TOPIC_ARN APP_URL TEST_RECIPIENT; do
	eval "value=\${$name:-}"
	[ -n "$value" ] || missing="$missing $name"
done
if [ -n "$missing" ]; then
	fail "these are not set:$missing
      docs/ses-smoke.md lists what each one is for."
fi
case "$APP_URL" in
https://*) : ;;
*) fail "APP_URL must be https://… so SNS can reach the webhook (got '$APP_URL')" ;;
esac
pass "the environment names everything the run needs"

# The credentials go to the CLI the way the deployment takes them: the same variables, not AWS_*.
export SES_REGION SES_ACCESS_KEY_ID SES_SECRET_ACCESS_KEY
export SES_CONFIGURATION_SET SES_SNS_TOPIC_ARN APP_URL

say "the sending domain has to be verified"
if step_ok; then
	domains=$(run domains)
	printf '%s\n' "$domains"
	printf '%s' "$domains" | grep -q "verified" || fail "no verified sending identity: publish the
      DNS records `mailcheap domains` prints, wait for SES to see them, and run this again"
	from=$(printf '%s' "$domains" | awk '/verified/{print $1; exit}')
	pass "the sending domain is verified ($from)"
else
	run domains
	from="${FROM_DOMAIN:-example.net}"
fi

say "a newsletter, a contact and an issue"
run newsletter create --name 'SES smoke' --slug "$SLUG" --from-name 'Jane Doe' \
	--from-email "jane@$from" --postal-address "${POSTAL_ADDRESS:-1 Main St, Paris}" >/dev/null 2>&1 || true
run contact add "$TEST_RECIPIENT" --newsletter "$SLUG" >/dev/null 2>&1 || true

# A body with one link to rewrite and nothing else special: the footer, the pixel and the
# unsubscribe link are the renderer's.
body=$(mktemp)
cat >"$body" <<'EOF'
# Hello from the SES smoke test

Read [this](https://example.com/read).
EOF
run campaign create --newsletter "$SLUG" --subject "$SUBJECT" --file "$body" \
	--preview-text 'The SES smoke test' >/dev/null 2>&1 || true
run campaign check --subject "$SUBJECT" || true

say "sending one copy to $TEST_RECIPIENT"
run campaign send --subject "$SUBJECT" --yes

say "waiting for SES to report the delivery through SNS"
warn "this is the part that proves the webhook: the Delivery event has to come back to $APP_URL"
if step_ok; then
	waited=0
	delivered=""
	while [ "$waited" -lt "$SNS_WAIT" ]; do
		delivered=$(run campaign report --subject "$SUBJECT" --json || true)
		case "$delivered" in
		*'"Delivered": 1'*) break ;;
		esac
		waited=$((waited + 5))
		sleep 5
	done
	case "$delivered" in
	*'"Delivered": 1'*) pass "SNS reported the delivery" ;;
	*)
		fail "no delivery event after ${SNS_WAIT}s. Check, in this order: the configuration set is
      the one SES used; its event destination publishes Delivery to the topic in
      SES_SNS_TOPIC_ARN; the subscription is confirmed; the webhook answers 200 (the serve log
      shows every event it rejects)."
		;;
	esac
fi

ask "open the message in $TEST_RECIPIENT now, click the link in it, and click the unsubscribe link."
ask "Then come back here and press return."
if [ "$DRY_RUN" != "1" ]; then
	read -r _ || true
fi

say "what the deployment recorded"
if step_ok; then
	report=$(run campaign report --subject "$SUBJECT" --json)
	printf '%s\n' "$report"
	printf '%s' "$report" | grep -q '"Opened": 1' || warn "no open recorded: the pixel did not load,
      or the mail client blocked it (some do until you allow images)"
	printf '%s' "$report" | grep -q '"Clicked": 1' || warn "no click recorded: a click within 30s of
      the send counts as a scanner, and curl-like User-Agents are ignored on purpose"
	printf '%s' "$report" | grep -q '"Unsubscribed": 1' || warn "no unsubscribe recorded: the reader
      has to press the link, and the click has to come from their browser"
	contact=$(run contact show "$TEST_RECIPIENT" --newsletter "$SLUG" --json)
	printf '%s\n' "$contact"
	printf '%s' "$contact" | grep -q '"Status": "unsubscribed"' || warn "the contact is not
      unsubscribed: check that the unsubscribe page answered, and that the token's signature
      verifies (a SESSION_SECRET that changed since the mail went out breaks every link in it)"
fi

say "done"
printf 'Open the pixel, the click and the unsubscribe are the three things this run exists to see;\n'
printf 'the notes above say which of them did not come back.\n'
