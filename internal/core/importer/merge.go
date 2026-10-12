package importer

import "nibwire/internal/core/model"

// Write is what one imported row asks for. The caller (the store, phase 2) applies it: an Insert
// adds the contact with the row's status, an UpdateID moves an existing contact's status forward.
type Write struct {
	// Insert is a contact the newsletter doesn't have yet, with the row's status and imported
	// history.
	Insert *Row
	// UpdateID is an existing contact to change; "" when the contact is new, or when the row
	// changes nothing about it.
	UpdateID string
	// NewStatus is the status to move the contact to; "" = leave it alone.
	NewStatus model.ContactStatus
	// Suppress is the global suppression this write must also upsert (upstream writes Suppression
	// for bounced/complained imports): model.SuppressBounce or model.SuppressComplaint, else "".
	// The caller upserts (email, reason), detail = import source, on the contact's email
	// (Insert.Email, or the existing contact's). "" means no suppression write.
	Suppress model.SuppressionReason
}

// Merge decides the writes for one newsletter's import. It adds missing contacts, only ever makes
// a status stricter, never reactivates a contact, and leaves a contact waiting to confirm as it is.
//
// Upstream also refreshes the imported engagement history (emails received, open and click rate) of
// existing contacts on every import; Write has no field for that, so an existing contact is only
// written when its status changes.
func Merge(rows []Row, existing map[string]model.Contact) []Write {
	writes := make([]Write, 0, len(rows))
	for _, row := range rows {
		write := Write{}
		if contact, found := existing[row.Email]; found {
			if newRank, ok := statusRank[row.Status]; ok {
				if oldRank, ok := statusRank[contact.Status]; ok && newRank > oldRank {
					write.UpdateID = contact.ID
					write.NewStatus = row.Status
				}
			}
		} else {
			insert := row
			write.Insert = &insert
		}
		switch row.Status {
		case model.ContactBounced:
			write.Suppress = model.SuppressBounce
		case model.ContactComplained:
			write.Suppress = model.SuppressComplaint
		}
		writes = append(writes, write)
	}
	return writes
}
