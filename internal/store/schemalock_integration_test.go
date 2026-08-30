//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// schemaLockKey serializes integration tests across packages: tests that
// rebuild the schema take it exclusively, every other test shares it.
const schemaLockKey = 7371

func lockSchema(t *testing.T, ownerURL string, exclusive bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		t.Fatalf("schema lock connect: %v", err)
	}
	fn := "pg_advisory_lock_shared"
	if exclusive {
		fn = "pg_advisory_lock"
	}
	if _, err := conn.Exec(ctx, "select "+fn+"($1)", schemaLockKey); err != nil {
		t.Fatalf("schema lock: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
}
