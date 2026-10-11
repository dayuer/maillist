// Package importer maps a CSV export from another provider onto Mailcheap contacts.
package importer

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mailcheap/internal/core/model"
)

// MaxRowsPerRequest caps one API/MCP import call (upstream MAX_IMPORT_ROWS_PER_REQUEST). The CLI
// streams a CSV file in chunks of this size, so files of any length import.
const MaxRowsPerRequest = 1000

// Upstream csv-import.js has its own, looser email check than lib/validation.js.
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

const maxEmailLength = 254

// columnAliases mirrors upstream COLUMN_ALIASES; the first header matching one of a field's aliases
// wins.
var columnAliases = map[string][]string{
	"email":          {"email", "emailaddress", "subscriberemail", "subscriber"},
	"status":         {"status", "subscriptionstatus", "subscriberstatus"},
	"subscribedAt":   {"createdat", "created", "subscribedat", "subscribedate", "subscriptiondate", "datesubscribed", "signupdate"},
	"emailsReceived": {"emailsreceived", "totalemailsreceived", "received"},
	"openRate":       {"openrate", "uniqueopenrate", "totalopenrate"},
	"clickRate":      {"clickrate", "clickthroughrate", "uniqueclickrate", "ctr"},
}

// statusAliases maps a file's status cell the way upstream STATUS_ALIASES does.
var statusAliases = map[string]model.ContactStatus{
	"active": model.ContactActive, "subscribed": model.ContactActive,
	"inactive": model.ContactUnsubscribed, "unsubscribed": model.ContactUnsubscribed,
	"bounced": model.ContactBounced, "invalid": model.ContactBounced, "hardbounce": model.ContactBounced,
	"complained": model.ContactComplained, "spam": model.ContactComplained, "spamreport": model.ContactComplained,
	"pending": model.ContactDormant, "validating": model.ContactDormant,
	"needsattention": model.ContactDormant, "dormant": model.ContactDormant,
}

// statusRank is upstream STATUS_RANK. A contact waiting to confirm (pending) has no rank, so
// nothing an import says can move it.
var statusRank = map[model.ContactStatus]int{
	model.ContactActive:       0,
	model.ContactDormant:      1,
	model.ContactUnsubscribed: 2,
	model.ContactBounced:      3,
	model.ContactComplained:   4,
}

// Columns are header indexes into a CSV record; -1 = the column is absent.
type Columns struct{ Email, Status, SubscribedAt, EmailsReceived, OpenRate, ClickRate int }

// MapColumns finds the column of each field from the file's header row.
func MapColumns(headers []string) Columns {
	normalized := make([]string, len(headers))
	for i, header := range headers {
		normalized[i] = normalizeKey(header)
	}
	index := func(field string) int {
		for i, key := range normalized {
			for _, alias := range columnAliases[field] {
				if key == alias {
					return i
				}
			}
		}
		return -1
	}
	return Columns{
		Email:          index("email"),
		Status:         index("status"),
		SubscribedAt:   index("subscribedAt"),
		EmailsReceived: index("emailsReceived"),
		OpenRate:       index("openRate"),
		ClickRate:      index("clickRate"),
	}
}

// NormalizeStatus maps a CSV status cell: "" or whitespace → active, a recognized alias → that
// status, anything else → dormant. It never fails (no bool: upstream has no failure branch).
func NormalizeStatus(value string) model.ContactStatus {
	if strings.TrimSpace(value) == "" {
		return model.ContactActive
	}
	if status, ok := statusAliases[normalizeKey(value)]; ok {
		return status
	}
	return model.ContactDormant
}

// NormalizeRate reads "45%", "0.45" and "45" as 0.45, capped at 1.
func NormalizeRate(value string) (float64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	// Upstream's Number.parseFloat reads a numeric prefix; strconv is strict and rejects the rest.
	number, err := strconv.ParseFloat(strings.Replace(text, "%", "", 1), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	if strings.Contains(text, "%") || number > 1 {
		number /= 100
	}
	return math.Min(number, 1), true
}

// Row is one normalized CSV record.
type Row struct {
	Email          string
	Status         model.ContactStatus // file status, or statusOverride when it ranks stricter
	SubscribedAt   *time.Time
	EmailsReceived *int
	OpenRate       *float64
	ClickRate      *float64
}

// NormalizeRow reads one record. ok is false when the email cell isn't usable.
func NormalizeRow(rec []string, cols Columns, statusOverride model.ContactStatus) (Row, bool) {
	email := normalizeEmail(cell(rec, cols.Email))
	if email == "" {
		return Row{}, false
	}

	fileStatus := model.ContactActive
	if cols.Status >= 0 {
		fileStatus = NormalizeStatus(cell(rec, cols.Status))
	}
	status := fileStatus
	if overrideRank, ok := statusRank[statusOverride]; ok {
		if fileRank, ok := statusRank[fileStatus]; ok && overrideRank > fileRank {
			status = statusOverride
		}
	}

	row := Row{Email: email, Status: status}
	if cols.SubscribedAt >= 0 {
		if at, ok := normalizeDate(cell(rec, cols.SubscribedAt)); ok {
			row.SubscribedAt = &at
		}
	}
	if cols.EmailsReceived >= 0 {
		if count, ok := normalizeCount(cell(rec, cols.EmailsReceived)); ok {
			row.EmailsReceived = &count
		}
	}
	if cols.OpenRate >= 0 {
		if rate, ok := NormalizeRate(cell(rec, cols.OpenRate)); ok {
			row.OpenRate = &rate
		}
	}
	if cols.ClickRate >= 0 {
		if rate, ok := NormalizeRate(cell(rec, cols.ClickRate)); ok {
			row.ClickRate = &rate
		}
	}
	return row, true
}

var nonDigits = regexp.MustCompile(`[^0-9]`)

// dateLayouts covers what a provider's export writes: an ISO instant or a plain calendar date.
var dateLayouts = []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"}

func normalizeKey(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeEmail is upstream csv-import.js normalizeEmail, which is looser than
// internal/core/validate's: a provider's export is a hint, not a signup.
func normalizeEmail(value string) string {
	email := strings.ToLower(strings.TrimSpace(value))
	if len(email) > maxEmailLength || !emailPattern.MatchString(email) {
		return ""
	}
	return email
}

func normalizeCount(value string) (int, bool) {
	digits := nonDigits.ReplaceAllString(value, "")
	if digits == "" {
		return 0, false
	}
	count, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	return count, true
}

func normalizeDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func cell(rec []string, index int) string {
	if index < 0 || index >= len(rec) {
		return ""
	}
	return rec[index]
}
