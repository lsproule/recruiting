// Package queue is the Postgres-backed job queue. Stage-entry side effects are
// enqueued here rather than run inline in a request.
//
// Jobs are enqueued inside the transaction that decides on them, so a
// rolled-back decision queues nothing. Every kind in Kinds() must have a
// handler: a worker started without one refuses to run rather than leaving
// those jobs unworked, and a handler under an unknown kind is refused the same
// way. Adding a kind means adding its args type and one registry line in
// kinds.go, then a handler wherever the worker's handler table is built.
//
// The queue library owns its own schema, applied by store.MigrateUp after the
// application's migrations. Those tables are infrastructure rather than tenant
// data: they carry no org_id and no row-level security, which is the one
// exemption from that invariant. A payload names the org whenever its handler
// needs a tenant-scoped transaction, as email.send does for email_log.
//
// email.send renders a template from internal/mail with the org's name as its
// only branding, records the attempt in email_log (queued, sent, failed), and
// delivers it over SMTP_URL. A failure is retried with backoff up to
// MaxAttempts; a template variable the payload does not carry fails the render
// before anything is sent. The same message is never delivered twice: a log
// row already reading sent stops the attempt, and bookkeeping that fails after
// SMTP accepted the message is logged rather than retried.
//
// A payload is personal data — a password reset link, a candidate's name and
// address — and a worked job keeps its payload in the queue's table. Retention
// is therefore deliberately short (see CompletedRetention and
// DiscardedRetention) so the queue does not become a store of it.
package queue
