package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Stages returns a job's pipeline in order. Any org user may read it; only
// recruiters and admins may change it.
func (s *JobService) Stages(ctx context.Context, p Principal, jobID uuid.UUID) ([]domain.Stage, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []domain.Stage
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var err error
		out, err = listStages(ctx, tx, jobID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list stages: %w", err)
	}
	return out, nil
}

// AddStage appends a stage to the end of the job's pipeline.
func (s *JobService) AddStage(ctx context.Context, p Principal, jobID uuid.UUID, in StageInput) (domain.Stage, error) {
	if err := requireRecruiter(p); err != nil {
		return domain.Stage{}, err
	}
	in, err := cleanStage(in)
	if err != nil {
		return domain.Stage{}, err
	}
	var out domain.Stage
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if err := requireJob(ctx, tx, jobID); err != nil {
			return err
		}
		existing, err := listStages(ctx, tx, jobID)
		if err != nil {
			return err
		}
		row, err := tx.Q.CreateStage(ctx, createStageParams(p.OrgID, jobID, int32(len(existing)+1), in.stage()))
		if err != nil {
			return err
		}
		out = toStage(row)
		// A stage added after the terminals would sit past the end of the
		// pipeline, so it goes in front of the first one that closes.
		ordered := insertBeforeTerminals(existing, out)
		if err := domain.ValidatePipeline(ordered); err != nil {
			return err
		}
		if err := renumber(ctx, tx, ordered); err != nil {
			return err
		}
		out.Position = positionOf(ordered, out.ID)
		return nil
	})
	if err != nil {
		return domain.Stage{}, unwrapPipeline("add stage", err)
	}
	return out, nil
}

// UpdateStage renames a stage and changes its kind, outcome, and unblind flag.
func (s *JobService) UpdateStage(ctx context.Context, p Principal, jobID, stageID uuid.UUID, in StageInput) (domain.Stage, error) {
	if err := requireRecruiter(p); err != nil {
		return domain.Stage{}, err
	}
	in, err := cleanStage(in)
	if err != nil {
		return domain.Stage{}, err
	}
	var out domain.Stage
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := stageOfJob(ctx, tx, jobID, stageID); err != nil {
			return err
		}
		st := in.stage()
		row, err := tx.Q.UpdateStage(ctx, db.UpdateStageParams{
			ID: stageID, Name: st.Name, Kind: string(st.Kind),
			TerminalStatus: terminalParam(st.Terminal), Unblind: st.Unblind,
			DefaultVetterID: vetterParam(st.DefaultVetterID),
			InterviewFormat: formatParam(st.InterviewFormat), DurationMinutes: int32Ptr(st.DurationMinutes),
			RoundSeconds: int32Ptr(st.RoundSeconds), BreakSeconds: int32Ptr(st.BreakSeconds),
		})
		if err != nil {
			return err
		}
		out = toStage(row)
		after, err := listStages(ctx, tx, jobID)
		if err != nil {
			return err
		}
		return domain.ValidatePipeline(after)
	})
	if err != nil {
		return domain.Stage{}, unwrapPipeline("update stage", err)
	}
	return out, nil
}

// DeleteStage removes a stage. A stage holding applications stays: moving them
// is a recruiter decision, not a side effect of editing the pipeline.
func (s *JobService) DeleteStage(ctx context.Context, p Principal, jobID, stageID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		stage, err := stageOfJob(ctx, tx, jobID, stageID)
		if err != nil {
			return err
		}
		held, err := tx.Q.CountApplicationsInStage(ctx, stageID)
		if err != nil {
			return err
		}
		if held > 0 {
			return fmt.Errorf("%w: %q holds %d", ErrStageOccupied, stage.Name, held)
		}
		if _, err := tx.Q.DeleteStage(ctx, stageID); err != nil {
			return err
		}
		remaining, err := listStages(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if err := domain.ValidatePipeline(remaining); err != nil {
			return err
		}
		return renumber(ctx, tx, remaining)
	})
	if err != nil {
		return unwrapPipeline("delete stage", err)
	}
	return nil
}

// ReorderStages sets the pipeline order. order must list every stage of the
// job exactly once so a dropped id cannot silently orphan a stage.
func (s *JobService) ReorderStages(ctx context.Context, p Principal, jobID uuid.UUID, order []uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if err := requireJob(ctx, tx, jobID); err != nil {
			return err
		}
		current, err := listStages(ctx, tx, jobID)
		if err != nil {
			return err
		}
		byID := make(map[uuid.UUID]domain.Stage, len(current))
		for _, st := range current {
			byID[st.ID] = st
		}
		if len(order) != len(current) {
			return fmt.Errorf("%w: the order lists %d of %d stages", domain.ErrInvalidPipeline, len(order), len(current))
		}
		reordered := make([]domain.Stage, 0, len(order))
		for _, id := range order {
			st, ok := byID[id]
			if !ok {
				return fmt.Errorf("%w: %s is not a stage of this job, or is listed twice", domain.ErrInvalidPipeline, id)
			}
			delete(byID, id)
			reordered = append(reordered, st)
		}
		return renumber(ctx, tx, reordered)
	})
	if err != nil {
		return unwrapPipeline("reorder stages", err)
	}
	return nil
}

// renumber writes positions 1..n in slice order. The (job_id, position) unique
// index is deferrable, so intermediate collisions during the rewrite are fine.
func renumber(ctx context.Context, tx *store.Tx, stages []domain.Stage) error {
	if _, err := tx.Exec(ctx, `set constraints all deferred`); err != nil {
		return err
	}
	for i, st := range stages {
		err := tx.Q.SetStagePosition(ctx, db.SetStagePositionParams{ID: st.ID, Position: int32(i + 1)})
		if err != nil {
			return err
		}
	}
	return nil
}

// copyTemplateStages copies a pipeline template into the job's own stage
// rows and records which template the job came from; the nil template id
// takes the org's default. Whatever terminal the template leaves out is
// appended, because a job without both terminals cannot close an
// application. It returns the template's id.
func copyTemplateStages(ctx context.Context, tx *store.Tx, orgID, jobID, templateID uuid.UUID) (uuid.UUID, error) {
	tmpl, err := pipelineTemplate(ctx, tx, orgID, templateID)
	if err != nil {
		return uuid.Nil, err
	}
	rows, err := tx.Q.ListPipelineTemplateStages(ctx, tmpl.ID)
	if err != nil {
		return uuid.Nil, err
	}
	specs := make([]domain.Stage, 0, len(rows)+2)
	var hired, rejected bool
	for _, row := range rows {
		st := templateStage(row)
		if st.Kind == domain.StageTerminal {
			if st.Terminal == domain.StatusHired && hired {
				continue
			}
			if st.Terminal == domain.StatusRejected && rejected {
				continue
			}
			hired = hired || st.Terminal == domain.StatusHired
			rejected = rejected || st.Terminal == domain.StatusRejected
		}
		specs = append(specs, st)
	}
	if !hired {
		specs = append(specs, domain.Stage{Name: "Hired", Kind: domain.StageTerminal, Terminal: domain.StatusHired})
	}
	if !rejected {
		specs = append(specs, domain.Stage{Name: "Rejected", Kind: domain.StageTerminal, Terminal: domain.StatusRejected})
	}

	copied := make([]domain.Stage, 0, len(specs))
	for i, st := range specs {
		row, err := tx.Q.CreateStage(ctx, createStageParams(orgID, jobID, int32(i+1), st))
		if err != nil {
			return uuid.Nil, fmt.Errorf("copy stage %q: %w", st.Name, err)
		}
		copied = append(copied, toStage(row))
	}
	if err := domain.ValidatePipeline(copied); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Q.SetJobTemplate(ctx, db.SetJobTemplateParams{ID: jobID, TemplateID: uuid.NullUUID{UUID: tmpl.ID, Valid: true}}); err != nil {
		return uuid.Nil, err
	}
	return tmpl.ID, nil
}

// templateStage reads a template stage as a pipeline stage. A terminal row
// seeded before template stages carried an outcome reads it off its name.
func templateStage(row db.PipelineTemplateStage) domain.Stage {
	st := domain.Stage{
		ID: row.ID, Position: int(row.Position), Name: row.Name, Kind: domain.StageKind(row.Kind), Unblind: row.Unblind,
		InterviewFormat: row.InterviewFormat, DurationMinutes: derefInt32(row.DurationMinutes),
		RoundSeconds: derefInt32(row.RoundSeconds), BreakSeconds: derefInt32(row.BreakSeconds),
	}
	if st.Kind == domain.StageTerminal {
		st.Terminal = domain.TerminalOutcomeFor(row.Name)
		if row.TerminalStatus != nil {
			st.Terminal = domain.ApplicationStatus(*row.TerminalStatus)
		}
	}
	return domain.NormalizeStage(st)
}

// createStageParams is a normalised stage as an insert.
func createStageParams(orgID, jobID uuid.UUID, position int32, st domain.Stage) db.CreateStageParams {
	return db.CreateStageParams{
		OrgID: orgID, JobID: jobID, Position: position,
		Name: st.Name, Kind: string(st.Kind), TerminalStatus: terminalParam(st.Terminal), Unblind: st.Unblind,
		DefaultVetterID: vetterParam(st.DefaultVetterID),
		InterviewFormat: formatParam(st.InterviewFormat), DurationMinutes: int32Ptr(st.DurationMinutes),
		RoundSeconds: int32Ptr(st.RoundSeconds), BreakSeconds: int32Ptr(st.BreakSeconds),
	}
}

// pipelineTemplate loads the named template, or the org's default when the
// id is nil.
func pipelineTemplate(ctx context.Context, tx *store.Tx, orgID, templateID uuid.UUID) (db.PipelineTemplate, error) {
	var tmpl db.PipelineTemplate
	var err error
	if templateID == uuid.Nil {
		tmpl, err = tx.Q.GetDefaultPipelineTemplate(ctx, orgID)
	} else {
		tmpl, err = tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: templateID, OrgID: orgID})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PipelineTemplate{}, ErrNoTemplate
	}
	return tmpl, err
}

func cleanStage(in StageInput) (StageInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return in, fmt.Errorf("%w: every stage needs a name", domain.ErrInvalidPipeline)
	}
	if !in.Kind.Valid() {
		return in, fmt.Errorf("%w: %q is not a stage kind", domain.ErrInvalidPipeline, in.Kind)
	}
	if in.Kind == domain.StageTerminal && in.Terminal != domain.StatusHired && in.Terminal != domain.StatusRejected {
		return in, fmt.Errorf("%w: terminal stage %q must close applications as hired or rejected", domain.ErrInvalidPipeline, in.Name)
	}
	st := in.stage()
	if err := domain.ValidateStageSettings(st); err != nil {
		return in, err
	}
	return stageInputOf(st), nil
}

// requireJob refuses a job id the org cannot see, so an unknown id is a 404
// rather than a silent no-op on an empty stage list.
func requireJob(ctx context.Context, tx *store.Tx, jobID uuid.UUID) error {
	if _, err := tx.Q.GetJob(ctx, jobID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

// insertBeforeTerminals places added in front of the first terminal stage,
// keeping the closing stages last where a pipeline reads them.
func insertBeforeTerminals(existing []domain.Stage, added domain.Stage) []domain.Stage {
	at := len(existing)
	if added.Kind != domain.StageTerminal {
		for i, s := range existing {
			if s.Kind == domain.StageTerminal {
				at = i
				break
			}
		}
	}
	out := make([]domain.Stage, 0, len(existing)+1)
	out = append(out, existing[:at]...)
	out = append(out, added)
	return append(out, existing[at:]...)
}

func positionOf(stages []domain.Stage, id uuid.UUID) int {
	for i, s := range stages {
		if s.ID == id {
			return i + 1
		}
	}
	return 0
}

func listStages(ctx context.Context, tx *store.Tx, jobID uuid.UUID) ([]domain.Stage, error) {
	rows, err := tx.Q.ListStages(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Stage, 0, len(rows))
	for _, row := range rows {
		out = append(out, toStage(row))
	}
	return out, nil
}

// stageOfJob refuses a stage id that belongs to another job, so a guessed id
// cannot edit someone else's pipeline.
func stageOfJob(ctx context.Context, tx *store.Tx, jobID, stageID uuid.UUID) (domain.Stage, error) {
	stages, err := listStages(ctx, tx, jobID)
	if err != nil {
		return domain.Stage{}, err
	}
	for _, st := range stages {
		if st.ID == stageID {
			return st, nil
		}
	}
	return domain.Stage{}, ErrNotFound
}

func toStage(row db.Stage) domain.Stage {
	st := domain.Stage{
		ID: row.ID, Position: int(row.Position), Name: row.Name,
		Kind: domain.StageKind(row.Kind), Unblind: row.Unblind,
		InterviewFormat: row.InterviewFormat, DurationMinutes: derefInt32(row.DurationMinutes),
		RoundSeconds: derefInt32(row.RoundSeconds), BreakSeconds: derefInt32(row.BreakSeconds),
	}
	if row.TerminalStatus != nil {
		st.Terminal = domain.ApplicationStatus(*row.TerminalStatus)
	}
	if row.DefaultVetterID.Valid {
		st.DefaultVetterID = row.DefaultVetterID.UUID
	}
	return domain.NormalizeStage(st)
}

// formatParam stores an interview format; the column is not null, so a stage
// of another kind carries the default rather than nothing.
func formatParam(format string) string {
	if format == "" {
		return domain.FormatCall
	}
	return format
}

// int32Ptr stores a setting a kind does not use as null.
func int32Ptr(n int) *int32 {
	if n == 0 {
		return nil
	}
	v := int32(n)
	return &v
}

func derefInt32(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func vetterParam(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func terminalParam(s domain.ApplicationStatus) *string {
	if s == "" {
		return nil
	}
	v := string(s)
	return &v
}

// unwrapPipeline keeps the errors a form shows inline unwrapped; anything else
// gains the operation that failed.
func unwrapPipeline(what string, err error) error {
	// A concurrent application landing in the stage trips the foreign key
	// instead of the count; both mean the stage is still in use.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation && pgErr.ConstraintName == "application_stage_id_fkey" {
		return ErrStageOccupied
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalidPipeline),
		errors.Is(err, ErrStageOccupied),
		errors.Is(err, ErrNotFound),
		errors.Is(err, ErrNoTemplate):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
