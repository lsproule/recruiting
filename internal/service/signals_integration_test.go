//go:build integration

package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/internal/domain/signals"
	"recruiting/internal/queue"
	"recruiting/internal/service"
)

// Get serves the object so the signal computation can read the compacted
// recording back.
func (b *fakeBlob) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects[key]
	if !ok {
		return nil, errors.New("blob: no such key")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// pasteSession records a session on the fixture's first problem where the
// solution is pasted from outside the page and submitted right after.
func (f *executionFixture) pasteSession(t *testing.T, att service.Attempt) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	cand := f.candidateOf(att.ID)
	p := f.problems[0]
	base := f.now.UnixMilli()
	source := "print(3)"
	events := []service.AttemptEvent{
		{Seq: 1, T: base, ProblemID: p.ID, Type: "focus", Data: json.RawMessage(`{}`)},
		{Seq: 2, T: base + 5000, ProblemID: p.ID, Type: "paste", Data: json.RawMessage(`{"len":8,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","internal":false}`)},
		{Seq: 3, T: base + 5100, ProblemID: p.ID, Type: "edit", Data: json.RawMessage(`{"changes":[[0,"print(3)"]]}`)},
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, events); err != nil {
		t.Fatalf("record: %v", err)
	}
	f.now = f.now.Add(20 * time.Second)
	sub, err := f.attempts.Submit(ctx, cand, att.ID, p.ID, "python", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.execute(t, passExecutor{}, sub.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.attempts.RecordEvents(ctx, cand, att.ID, []service.AttemptEvent{
		{Seq: 4, T: f.now.UnixMilli(), ProblemID: p.ID, Type: "submit", Data: json.RawMessage(`{"submission_id":"` + sub.ID.String() + `"}`)},
	}); err != nil {
		t.Fatalf("record submit: %v", err)
	}
	if _, err := f.attempts.Finish(ctx, cand, att.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	return sub.ID
}

func (f *executionFixture) computeSignals(t *testing.T, b service.BlobReader, attemptID uuid.UUID) error {
	t.Helper()
	payload, _ := json.Marshal(service.SignalsComputePayload{AttemptID: attemptID, OrgID: f.orgID})
	h := service.SignalsComputeHandler(f.st, b, nil)
	return h(context.Background(), queue.Job{Kind: queue.KindSignalsCompute, Payload: payload})
}

type signalRow struct {
	value, weight float64
	confidence    string
	evidence      []signals.Evidence
}

func (f *executionFixture) signalRows(t *testing.T, attemptID uuid.UUID) (map[string]signalRow, *float64) {
	t.Helper()
	ctx := context.Background()
	rows, err := f.sys.Query(ctx, `select name, value, weight, confidence, evidence from integrity_signal where attempt_id = $1`, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]signalRow{}
	for rows.Next() {
		var name string
		var r signalRow
		var raw []byte
		if err := rows.Scan(&name, &r.value, &r.weight, &r.confidence, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.evidence); err != nil {
			t.Fatalf("evidence %s: %v", raw, err)
		}
		out[name] = r
	}
	var risk *float64
	if err := f.sys.QueryRow(ctx, `select risk_score from attempt where id = $1`, attemptID).Scan(&risk); err != nil {
		t.Fatal(err)
	}
	return out, risk
}

func TestSignalsComputeStoresEverySignalAndTheRiskScore(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Signalled Adder"))
	att := f.start(t)
	f.pasteSession(t, att)
	blob := newFakeBlob()
	if err := f.finalize(t, blob, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := f.computeSignals(t, blob, att.ID); err != nil {
		t.Fatalf("signals.compute: %v", err)
	}

	rows, risk := f.signalRows(t, att.ID)
	if len(rows) != len(service.IntegritySignalNames) {
		t.Fatalf("%d signal rows, want %d: %+v", len(rows), len(service.IntegritySignalNames), rows)
	}
	if r := rows["paste_ratio"]; r.value != 1 || r.weight != 25 || r.confidence != "normal" || len(r.evidence) != 1 {
		t.Errorf("paste_ratio = %+v, want 1 at weight 25 with evidence", r)
	}
	if r := rows["paste_then_pass"]; r.value != 1 {
		t.Errorf("paste_then_pass = %+v, want 1", r)
	}
	// The pasted source is the reference solution itself.
	if r := rows["reference_similarity"]; r.value < 0.9 {
		t.Errorf("reference_similarity = %+v, want a copy of the reference", r)
	}
	if risk == nil || *risk < 60 || *risk > 100 {
		t.Fatalf("risk = %v, want a paste-driven score", risk)
	}

	// Recomputing replaces the rows rather than duplicating them, and a
	// reweighted org changes the score.
	if _, err := f.sys.Exec(context.Background(),
		`insert into org_setting (org_id, key, value) values ($1, 'integrity_weights', $2)
		 on conflict (org_id, key) do update set value = excluded.value`,
		f.orgID, `{"paste_ratio":0,"paste_then_pass":0,"burst_typing":10,"edit_ratio":10,"blur_then_solution":15,"speed_vs_difficulty":5,"reference_similarity":10}`); err != nil {
		t.Fatal(err)
	}
	if err := f.computeSignals(t, blob, att.ID); err != nil {
		t.Fatalf("second signals.compute: %v", err)
	}
	rows, again := f.signalRows(t, att.ID)
	if len(rows) != len(service.IntegritySignalNames) {
		t.Fatalf("%d signal rows after recompute, want %d", len(rows), len(service.IntegritySignalNames))
	}
	if r := rows["paste_ratio"]; r.weight != 0 {
		t.Errorf("paste_ratio weight after reweighting = %v, want 0", r.weight)
	}
	if again == nil || *again >= *risk {
		t.Errorf("risk after removing the paste weights = %v, want below %v", again, *risk)
	}
}

// Without object storage the recording is still in Postgres, and a gap in
// it lowers confidence without stopping the computation.
func TestSignalsComputeFallsBackToTheEventRowsAndFlagsAGap(t *testing.T) {
	f := newExecutionFixture(t, adderImport(t, "Gappy Signalled Adder"))
	att := f.start(t)
	cand := f.candidateOf(att.ID)
	if _, err := f.attempts.RecordEvents(context.Background(), cand, att.ID, []service.AttemptEvent{
		{Seq: 5, T: f.now.UnixMilli(), ProblemID: f.problems[0].ID, Type: "paste", Data: json.RawMessage(`{"len":8,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","internal":false}`)},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := f.attempts.Finish(context.Background(), cand, att.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := f.finalize(t, nil, att.ID, 0); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := f.computeSignals(t, nil, att.ID); err != nil {
		t.Fatalf("signals.compute: %v", err)
	}
	rows, risk := f.signalRows(t, att.ID)
	if r := rows["paste_ratio"]; r.confidence != "low" || r.value != 1 {
		t.Errorf("paste_ratio = %+v, want 1 at low confidence", r)
	}
	if risk == nil || *risk < 25 {
		t.Errorf("risk = %v; a gap must not suppress the score", risk)
	}
}
