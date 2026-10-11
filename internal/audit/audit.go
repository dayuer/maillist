// Package audit records who did what. It is the third layer of the permission model (design §11):
// credentials say what you may do, the audit log says what you did.
//
// Recording is best-effort by design. By the time this runs the action has already happened, so a
// failed insert is logged and never turned into an error the operator would read as "nothing
// happened" — the alternative is more dangerous than the missing row.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/store"
)

// Actor is who is acting.
type Actor struct {
	Kind string // cli | mcp | api | system
	Name string // "cli", "mcp:newsletter-bot", "api:site"
}

// CLI is the actor every command-line write uses.
func CLI() Actor { return Actor{Kind: "cli", Name: "cli"} }

// API is the actor the v1 API uses, named by the credential behind it.
func API(credentialName string) Actor { return Actor{Kind: "api", Name: "api:" + credentialName} }

// MCP is the actor the MCP server uses.
func MCP(credentialName string) Actor { return Actor{Kind: "mcp", Name: "mcp:" + credentialName} }

// ParamsDigest is the hex SHA-256 of the canonical JSON of the arguments.
//
// Callers pass a map[string]any, never a struct: encoding/json sorts a map's keys and keeps a
// struct's field order, and the digest has to be the same for the same arguments however they were
// assembled. nil is the digest of "", for an action that takes none.
func ParamsDigest(params any) string {
	if params == nil {
		return ""
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		// A caller handed something that cannot be marshalled (a channel, a func). Record that
		// rather than nothing: the digest is evidence, and an empty one would look like "no
		// arguments".
		encoded = []byte("<unencodable: " + err.Error() + ">")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Record writes one audit row. It returns the error as well as logging it, so a caller that wants to
// treat it as fatal can, and the CLI's helper below does not.
func Record(ctx context.Context, db *store.DB, actor Actor, action, result string, params any, log *slog.Logger) error {
	entry := store.AuditEntry{
		ID: ids.New(), ActorKind: actor.Kind, ActorName: actor.Name,
		Action: action, ParamsDigest: ParamsDigest(params), Result: result,
		CreatedAt: time.Now().UTC(),
	}
	if err := db.InsertAudit(ctx, entry); err != nil {
		if log != nil {
			log.Error("audit", "action", action, "error", err)
		}
		return err
	}
	return nil
}

// Note records one action and swallows the failure, logging it. It is the shape every CLI write
// uses: the write has happened, and the audit row is a record of it rather than a condition of it.
func Note(ctx context.Context, db *store.DB, actor Actor, action, result string, params any, log *slog.Logger) {
	_ = Record(ctx, db, actor, action, result, params, log)
}

// List is `audit list`.
func List(ctx context.Context, db *store.DB, filter store.AuditFilter) ([]store.AuditEntry, error) {
	return db.AuditLog(ctx, filter)
}
