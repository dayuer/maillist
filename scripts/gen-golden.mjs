#!/usr/bin/env node
// Regenerates the golden vectors asserted by TestGoldenVectorsMatchOriginal
// (docs/superpowers/plans/2026-10-11-mailcheap-go-rewrite-plan.md, Task 1.4).
//
// The plan claims the Go signer is byte-for-byte identical to the original Node
// implementation (marclou/mailcheap@67d13b0). The constants in the plan are the
// only evidence, and they were produced by hand once. This script re-derives them
// so the claim stays auditable.
//
// The algorithm below is a transcription of the upstream sources -- it reads
// third_party/upstream/ for reference but never imports or runs it:
//   lib/tracking.js       getOpenUrl / getClickUrl (+ sign, SIGNATURE_LENGTH = 22)
//   lib/contact-token.js  createContactToken (info-labelled HMAC, payload with t)
//   lib/unsubscribe.js    createUnsubscribeToken (payload without t)
//
// Run: node scripts/gen-golden.mjs
// It exits non-zero if any derived vector differs from the constant in the plan.

import crypto from "node:crypto";

const SECRET = "test-session-secret-with-at-least-32-characters";
const BASE = "https://mail.example.com";
const MESSAGE_ID = "66f1a2b3c4d5e6f7a8b9c0d1";
const CONTACT_ID = "64f1a2b3c4d5e6f7a8b9c0d1";
const NEWSLETTER_ID = "74f1a2b3c4d5e6f7a8b9c0d2";
const CLICK_TARGET = "https://example.org/?utm_source=newsletter&ref=1";
const AT_MS = 1791000000000; // 2026-10-11-ish; the confirm/keep vectors pin this.

const SIGNATURE_LENGTH = 22; // lib/tracking.js

const INFO = {
	tracking: "mailcheap-tracking-v1",
	unsubscribe: "mailcheap-unsubscribe-v1",
	confirm: "mailcheap-confirm-v1",
	keep: "mailcheap-keep-v1",
};

// HKDF-SHA256 with an empty salt (RFC 5869: an absent/empty salt is HashLen zero bytes).
function key(info) {
	return Buffer.from(crypto.hkdfSync("sha256", SECRET, "", info, 32));
}

// lib/tracking.js: sign() truncates the base64url HMAC to SIGNATURE_LENGTH characters.
function trackingSign(value) {
	return crypto.createHmac("sha256", key(INFO.tracking)).update(value).digest("base64url").slice(0, SIGNATURE_LENGTH);
}

function openUrl(baseUrl, messageId) {
	const id = String(messageId).toLowerCase();
	return `${baseUrl}/o/${id}.${trackingSign(`open:${id}`)}`;
}

function clickUrl(baseUrl, messageId, url) {
	const id = String(messageId).toLowerCase();
	return `${baseUrl}/c/${id}.${trackingSign(`click:${id}:${url}`)}.${Buffer.from(url).toString("base64url")}`;
}

// lib/contact-token.js + lib/unsubscribe.js: base64url JSON payload + "." + full HMAC.
function sign(info, payload) {
	return crypto.createHmac("sha256", key(info)).update(payload).digest("base64url");
}

function createUnsubscribeToken({ contactId, newsletterId }) {
	const reference = {
		c: String(contactId ?? "").toLowerCase(),
		n: String(newsletterId ?? "").toLowerCase(),
	};
	const payload = Buffer.from(JSON.stringify(reference)).toString("base64url");
	return `${payload}.${sign(INFO.unsubscribe, payload)}`;
}

function createContactToken(info, { contactId, newsletterId }, nowMs) {
	// Key order c, n, t is load-bearing: JSON.stringify preserves insertion order.
	const reference = {
		c: String(contactId ?? "").toLowerCase(),
		n: String(newsletterId ?? "").toLowerCase(),
		t: Math.floor(nowMs / 1000),
	};
	const payload = Buffer.from(JSON.stringify(reference)).toString("base64url");
	return `${payload}.${sign(info, payload)}`;
}

// The five constants asserted in TestGoldenVectorsMatchOriginal.
const expected = {
	open: "https://mail.example.com/o/66f1a2b3c4d5e6f7a8b9c0d1.Kve15SMFSQgfp3EcKblC_w",
	click:
		"https://mail.example.com/c/66f1a2b3c4d5e6f7a8b9c0d1.eYRYRA0c6sDtnw0fNtufPs.aHR0cHM6Ly9leGFtcGxlLm9yZy8_dXRtX3NvdXJjZT1uZXdzbGV0dGVyJnJlZj0x",
	unsubscribe:
		"eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiJ9.apuRlFjYJ2ns-renATb2e45fLpGIBDe9xm_yHAJ7_rE",
	confirm:
		"eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.cNcx9tj972399hE6WeVQw9FNKAxI_iUvKZvXRtT4n78",
	keep:
		"eyJjIjoiNjRmMWEyYjNjNGQ1ZTZmN2E4YjljMGQxIiwibiI6Ijc0ZjFhMmIzYzRkNWU2ZjdhOGI5YzBkMiIsInQiOjE3OTEwMDAwMDB9.a2AcpJdkmOh8VfUsPyjKvRH4JZ6eC04BQNDDqS24ceI",
};

const ref = { contactId: CONTACT_ID, newsletterId: NEWSLETTER_ID };
const produced = {
	open: openUrl(BASE, MESSAGE_ID),
	click: clickUrl(BASE, MESSAGE_ID, CLICK_TARGET),
	unsubscribe: createUnsubscribeToken(ref),
	confirm: createContactToken(INFO.confirm, ref, AT_MS),
	keep: createContactToken(INFO.keep, ref, AT_MS),
};

let ok = true;
for (const name of Object.keys(expected)) {
	const match = produced[name] === expected[name];
	ok &&= match;
	console.log(`${match ? "ok  " : "FAIL"} ${name}: ${produced[name]}`);
	if (!match) console.log(`     want: ${expected[name]}`);
}
process.exit(ok ? 0 : 1);
