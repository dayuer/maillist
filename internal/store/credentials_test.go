package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"mailcheap/internal/core/ids"
	"mailcheap/internal/creds"
)

func seedCredential(t *testing.T, db *DB) (Credential, string) {
	t.Helper()
	generated := creds.Generate()
	credential := Credential{
		ID: ids.New(), Kind: "api", Name: "site", Prefix: generated.Prefix,
		TokenHash: generated.Hash, Scopes: []creds.Scope{creds.ScopeRead, creds.ScopeWrite},
		DailyBudget: 2000, CreatedAt: time.Now().UTC(),
	}
	if err := db.CreateCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	return credential, generated.Token
}

// 库里只有 hash：任何一列都不等于明文 token。
func TestCredentialsStoreOnlyTheHash(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	credential, token := seedCredential(t, db)

	rows, err := db.Rows(ctx, "SELECT * FROM credentials")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		for i, value := range values {
			if text, ok := value.(string); ok && text == token {
				t.Fatalf("column %s holds the token itself", columns[i])
			}
		}
	}
	if len(credential.TokenHash) != 64 {
		t.Fatalf("hash = %q, want a 64-character SHA-256", credential.TokenHash)
	}

	// The lookup a request makes finds it, and finds nothing for a token that is not there.
	found, err := db.CredentialByHash(ctx, creds.Hash(token))
	if err != nil || found.ID != credential.ID {
		t.Fatalf("by hash = %+v %v", found, err)
	}
	if _, err := db.CredentialByHash(ctx, creds.Hash("mc_not-a-real-token")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// 撤销之后查不到：这就是 token 失效的机制（D8，token 自身不带任何声明）。
func TestRevokedCredentialsAreNotFoundByHash(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	credential, token := seedCredential(t, db)

	revoked, err := db.RevokeCredential(ctx, credential.ID, time.Now().UTC())
	if err != nil || !revoked {
		t.Fatalf("revoke = %v %v", revoked, err)
	}
	if _, err := db.CredentialByHash(ctx, creds.Hash(token)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked credential was found: %v", err)
	}
	// Revoking twice reports that there was nothing to revoke.
	if again, err := db.RevokeCredential(ctx, credential.ID, time.Now().UTC()); err != nil || again {
		t.Fatalf("second revoke = %v %v", again, err)
	}
	// It is still in the list, so an operator can see what was taken away.
	all, err := db.Credentials(ctx)
	if err != nil || len(all) != 1 || !all[0].Revoked() {
		t.Fatalf("credentials = %+v %v", all, err)
	}
}

// last_used_at 是每分钟最多写一次的东西，不是每次请求的写。
func TestTouchCredentialRecordsUse(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	credential, _ := seedCredential(t, db)

	loaded, err := db.Credential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastUsedAt != nil {
		t.Fatalf("last used = %v before any use", loaded.LastUsedAt)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.TouchCredential(ctx, credential.ID, at); err != nil {
		t.Fatal(err)
	}
	if loaded, err = db.Credential(ctx, credential.ID); err != nil {
		t.Fatal(err)
	}
	if loaded.LastUsedAt == nil || !loaded.LastUsedAt.Equal(at) {
		t.Fatalf("last used = %v, want %v", loaded.LastUsedAt, at)
	}
	// Scopes and budget survive the touch.
	if len(loaded.Scopes) != 2 || loaded.Scopes[0] != creds.ScopeRead || loaded.DailyBudget != 2000 {
		t.Fatalf("credential = %+v", loaded)
	}
}

func TestCredentialByPrefix(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	credential, _ := seedCredential(t, db)

	found, err := db.CredentialByPrefix(ctx, credential.Prefix)
	if err != nil || found.ID != credential.ID {
		t.Fatalf("by prefix = %+v %v", found, err)
	}
	if _, err := db.CredentialByPrefix(ctx, "mc_nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
