// Package store is the RLS-aware Postgres persistence layer. It sets app.org_id per
// transaction; no query path outside the admin CLI and migrations bypasses
// RLS.
package store
