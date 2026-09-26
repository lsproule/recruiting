package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"recruiting/internal/domain"
	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

var (
	ErrProcessDefault = errors.New("service: the default process cannot be deleted; make another the default first")
	ErrProcessName    = errors.New("service: a process needs a name")
	ErrProcessSource  = errors.New("service: unknown process to copy from")
)

// Process is a hiring process: a pipeline template with its stages, and how
// many jobs were built from it.
type Process struct {
	ID          uuid.UUID
	Name        string
	Description string
	// LibraryKey names the built-in process this one was copied from, or is
	// empty for one written from scratch.
	LibraryKey string
	IsDefault  bool
	Stages     []domain.Stage
	Jobs       int
	UpdatedAt  time.Time
}

// ProcessInput is the process form: its name and what it is for.
type ProcessInput struct {
	Name        string
	Description string
}

// ProcessSource says where a new process's stages come from: a built-in
// library entry, an existing process of the org, or nothing.
type ProcessSource struct {
	LibraryKey string
	ProcessID  uuid.UUID
}

// ProcessService keeps the org's process library. Any org user may read it;
// recruiters and admins change it. Jobs copy a process at creation and are
// never touched by later edits.
type ProcessService struct{ st *store.Store }

func NewProcessService(st *store.Store) *ProcessService { return &ProcessService{st: st} }

// Library is the built-in processes a new one may be copied from.
func (s *ProcessService) Library() []domain.ProcessSpec { return domain.ProcessLibrary }

// List is every process of the org, the default first.
func (s *ProcessService) List(ctx context.Context, p Principal) ([]Process, error) {
	if p.Kind != PrincipalOrgUser {
		return nil, ErrForbidden
	}
	var out []Process
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListPipelineTemplates(ctx, p.OrgID)
		if err != nil {
			return err
		}
		counts, err := tx.Q.CountJobsByTemplate(ctx, p.OrgID)
		if err != nil {
			return err
		}
		jobs := make(map[uuid.UUID]int, len(counts))
		for _, c := range counts {
			if c.TemplateID.Valid {
				jobs[c.TemplateID.UUID] = int(c.Jobs)
			}
		}
		out = make([]Process, 0, len(rows))
		for _, row := range rows {
			proc, err := loadProcess(ctx, tx, row)
			if err != nil {
				return err
			}
			proc.Jobs = jobs[row.ID]
			out = append(out, proc)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return out, nil
}

// Get is one process with its stages.
func (s *ProcessService) Get(ctx context.Context, p Principal, id uuid.UUID) (Process, error) {
	if p.Kind != PrincipalOrgUser {
		return Process{}, ErrForbidden
	}
	var out Process
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		out, err = loadProcess(ctx, tx, row)
		if err != nil {
			return err
		}
		counts, err := tx.Q.CountJobsByTemplate(ctx, p.OrgID)
		if err != nil {
			return err
		}
		for _, c := range counts {
			if c.TemplateID.Valid && c.TemplateID.UUID == id {
				out.Jobs = int(c.Jobs)
			}
		}
		return nil
	})
	if err != nil {
		return Process{}, wrapProcess("get process", err)
	}
	return out, nil
}

// Create adds a process. Its stages come from the source: a library entry,
// another process, or, with neither, the bare pair of terminals every
// pipeline needs so the editor starts from something valid.
func (s *ProcessService) Create(ctx context.Context, p Principal, in ProcessInput, from ProcessSource) (Process, error) {
	if err := requireRecruiter(p); err != nil {
		return Process{}, err
	}
	in, err := cleanProcess(in)
	if err != nil {
		return Process{}, err
	}
	var out Process
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		var stages []domain.Stage
		var libraryKey *string
		switch {
		case from.LibraryKey != "":
			spec, ok := domain.ProcessByKey(from.LibraryKey)
			if !ok {
				return ErrProcessSource
			}
			stages = spec.PipelineStages()
			libraryKey = &spec.Key
			if in.Description == "" {
				in.Description = spec.Description
			}
		case from.ProcessID != uuid.Nil:
			src, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: from.ProcessID, OrgID: p.OrgID})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProcessSource
			}
			if err != nil {
				return err
			}
			proc, err := loadProcess(ctx, tx, src)
			if err != nil {
				return err
			}
			stages = proc.Stages
			if src.LibraryKey != nil {
				libraryKey = src.LibraryKey
			}
			if in.Description == "" {
				in.Description = src.Description
			}
		default:
			stages = []domain.Stage{
				{Name: "Applied", Kind: domain.StageGeneric},
				{Name: "Hired", Kind: domain.StageTerminal, Terminal: domain.StatusHired},
				{Name: "Rejected", Kind: domain.StageTerminal, Terminal: domain.StatusRejected},
			}
		}
		row, err := tx.Q.CreatePipelineTemplate(ctx, db.CreatePipelineTemplateParams{
			OrgID: p.OrgID, Name: in.Name, IsDefault: false, Description: in.Description, LibraryKey: libraryKey,
		})
		if err != nil {
			return err
		}
		for i, st := range stages {
			if _, err := tx.Q.CreateProcessStage(ctx, processStageParams(p.OrgID, row.ID, int32(i+1), domain.NormalizeStage(st))); err != nil {
				return err
			}
		}
		out, err = loadProcess(ctx, tx, row)
		return err
	})
	if err != nil {
		return Process{}, wrapProcess("create process", err)
	}
	return out, nil
}

// Update renames a process and changes its description.
func (s *ProcessService) Update(ctx context.Context, p Principal, id uuid.UUID, in ProcessInput) (Process, error) {
	if err := requireRecruiter(p); err != nil {
		return Process{}, err
	}
	in, err := cleanProcess(in)
	if err != nil {
		return Process{}, err
	}
	var out Process
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID}); err != nil {
			return err
		}
		row, err := tx.Q.UpdatePipelineTemplate(ctx, db.UpdatePipelineTemplateParams{ID: id, Name: in.Name, Description: in.Description})
		if err != nil {
			return err
		}
		out, err = loadProcess(ctx, tx, row)
		return err
	})
	if err != nil {
		return Process{}, wrapProcess("update process", err)
	}
	return out, nil
}

// SetDefault makes the process the one jobs are built from when nobody
// picks one.
func (s *ProcessService) SetDefault(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID}); err != nil {
			return err
		}
		if err := tx.Q.UnsetDefaultPipelineTemplates(ctx, p.OrgID); err != nil {
			return err
		}
		return tx.Q.SetPipelineTemplateDefault(ctx, id)
	})
	if err != nil {
		return wrapProcess("set default process", err)
	}
	return nil
}

// Delete removes a process. The default stays: an org must always have
// something to build a job from. Jobs built from it keep their stages.
func (s *ProcessService) Delete(ctx context.Context, p Principal, id uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		if row.IsDefault {
			return ErrProcessDefault
		}
		n, err := tx.Q.DeletePipelineTemplate(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return wrapProcess("delete process", err)
	}
	return nil
}

// AddStage appends a stage, in front of the terminals like a job's editor.
func (s *ProcessService) AddStage(ctx context.Context, p Principal, id uuid.UUID, in StageInput) (domain.Stage, error) {
	if err := requireRecruiter(p); err != nil {
		return domain.Stage{}, err
	}
	in, err := cleanStage(in)
	if err != nil {
		return domain.Stage{}, err
	}
	var out domain.Stage
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID}); err != nil {
			return err
		}
		existing, err := listProcessStages(ctx, tx, id)
		if err != nil {
			return err
		}
		row, err := tx.Q.CreateProcessStage(ctx, processStageParams(p.OrgID, id, int32(len(existing)+1), in.stage()))
		if err != nil {
			return err
		}
		out = templateStage(row)
		ordered := insertBeforeTerminals(existing, out)
		if err := domain.ValidatePipeline(ordered); err != nil {
			return err
		}
		if err := renumberProcess(ctx, tx, ordered); err != nil {
			return err
		}
		out.Position = positionOf(ordered, out.ID)
		return tx.Q.TouchPipelineTemplate(ctx, id)
	})
	if err != nil {
		return domain.Stage{}, wrapProcess("add stage", err)
	}
	return out, nil
}

// UpdateStage rewrites one stage of the process.
func (s *ProcessService) UpdateStage(ctx context.Context, p Principal, id, stageID uuid.UUID, in StageInput) (domain.Stage, error) {
	if err := requireRecruiter(p); err != nil {
		return domain.Stage{}, err
	}
	in, err := cleanStage(in)
	if err != nil {
		return domain.Stage{}, err
	}
	var out domain.Stage
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := processStageOf(ctx, tx, id, stageID); err != nil {
			return err
		}
		st := in.stage()
		row, err := tx.Q.UpdateProcessStage(ctx, db.UpdateProcessStageParams{
			ID: stageID, Name: st.Name, Kind: string(st.Kind), Unblind: st.Unblind,
			InterviewFormat: formatParam(st.InterviewFormat), DurationMinutes: int32Ptr(st.DurationMinutes),
			RoundSeconds: int32Ptr(st.RoundSeconds), BreakSeconds: int32Ptr(st.BreakSeconds),
			TerminalStatus: terminalParam(st.Terminal),
		})
		if err != nil {
			return err
		}
		out = templateStage(row)
		after, err := listProcessStages(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidatePipeline(after); err != nil {
			return err
		}
		return tx.Q.TouchPipelineTemplate(ctx, id)
	})
	if err != nil {
		return domain.Stage{}, wrapProcess("update stage", err)
	}
	return out, nil
}

// DeleteStage removes a stage from the process.
func (s *ProcessService) DeleteStage(ctx context.Context, p Principal, id, stageID uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := processStageOf(ctx, tx, id, stageID); err != nil {
			return err
		}
		if _, err := tx.Q.DeleteProcessStage(ctx, stageID); err != nil {
			return err
		}
		remaining, err := listProcessStages(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := domain.ValidatePipeline(remaining); err != nil {
			return err
		}
		if err := renumberProcess(ctx, tx, remaining); err != nil {
			return err
		}
		return tx.Q.TouchPipelineTemplate(ctx, id)
	})
	if err != nil {
		return wrapProcess("delete stage", err)
	}
	return nil
}

// ReorderStages sets the order; order must name every stage exactly once.
func (s *ProcessService) ReorderStages(ctx context.Context, p Principal, id uuid.UUID, order []uuid.UUID) error {
	if err := requireRecruiter(p); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetPipelineTemplate(ctx, db.GetPipelineTemplateParams{ID: id, OrgID: p.OrgID}); err != nil {
			return err
		}
		current, err := listProcessStages(ctx, tx, id)
		if err != nil {
			return err
		}
		reordered, err := applyOrder(current, order)
		if err != nil {
			return err
		}
		if err := renumberProcess(ctx, tx, reordered); err != nil {
			return err
		}
		return tx.Q.TouchPipelineTemplate(ctx, id)
	})
	if err != nil {
		return wrapProcess("reorder stages", err)
	}
	return nil
}

// applyOrder arranges current in the order given, refusing an order that
// misses or repeats a stage.
func applyOrder(current []domain.Stage, order []uuid.UUID) ([]domain.Stage, error) {
	byID := make(map[uuid.UUID]domain.Stage, len(current))
	for _, st := range current {
		byID[st.ID] = st
	}
	if len(order) != len(current) {
		return nil, fmt.Errorf("%w: the order lists %d of %d stages", domain.ErrInvalidPipeline, len(order), len(current))
	}
	out := make([]domain.Stage, 0, len(order))
	for _, id := range order {
		st, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s is not a stage of this pipeline, or is listed twice", domain.ErrInvalidPipeline, id)
		}
		delete(byID, id)
		out = append(out, st)
	}
	return out, nil
}

func renumberProcess(ctx context.Context, tx *store.Tx, stages []domain.Stage) error {
	if _, err := tx.Exec(ctx, `set constraints all deferred`); err != nil {
		return err
	}
	for i, st := range stages {
		if err := tx.Q.SetProcessStagePosition(ctx, db.SetProcessStagePositionParams{ID: st.ID, Position: int32(i + 1)}); err != nil {
			return err
		}
	}
	return nil
}

func loadProcess(ctx context.Context, tx *store.Tx, row db.PipelineTemplate) (Process, error) {
	stages, err := listProcessStages(ctx, tx, row.ID)
	if err != nil {
		return Process{}, err
	}
	out := Process{
		ID: row.ID, Name: row.Name, Description: row.Description, IsDefault: row.IsDefault,
		Stages: stages, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.LibraryKey != nil {
		out.LibraryKey = *row.LibraryKey
	}
	return out, nil
}

func listProcessStages(ctx context.Context, tx *store.Tx, templateID uuid.UUID) ([]domain.Stage, error) {
	rows, err := tx.Q.ListPipelineTemplateStages(ctx, templateID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Stage, 0, len(rows))
	for _, row := range rows {
		out = append(out, templateStage(row))
	}
	return out, nil
}

func processStageOf(ctx context.Context, tx *store.Tx, templateID, stageID uuid.UUID) (domain.Stage, error) {
	stages, err := listProcessStages(ctx, tx, templateID)
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

func processStageParams(orgID, templateID uuid.UUID, position int32, st domain.Stage) db.CreateProcessStageParams {
	return db.CreateProcessStageParams{
		OrgID: orgID, TemplateID: templateID, Position: position,
		Name: st.Name, Kind: string(st.Kind), Unblind: st.Unblind,
		InterviewFormat: formatParam(st.InterviewFormat), DurationMinutes: int32Ptr(st.DurationMinutes),
		RoundSeconds: int32Ptr(st.RoundSeconds), BreakSeconds: int32Ptr(st.BreakSeconds),
		TerminalStatus: terminalParam(st.Terminal),
	}
}

// seedProcess writes one library process for an org. It runs on the
// bootstrap's owner connection, before any principal exists.
func seedProcess(ctx context.Context, tx *store.Tx, orgID uuid.UUID, name string, spec domain.ProcessSpec, isDefault bool) (uuid.UUID, error) {
	key := spec.Key
	tmpl, err := tx.Q.CreatePipelineTemplate(ctx, db.CreatePipelineTemplateParams{
		OrgID: orgID, Name: name, IsDefault: isDefault, Description: spec.Description, LibraryKey: &key,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create pipeline template %q: %w", name, err)
	}
	for i, st := range spec.PipelineStages() {
		if _, err := tx.Q.CreateProcessStage(ctx, processStageParams(orgID, tmpl.ID, int32(i+1), st)); err != nil {
			return uuid.Nil, fmt.Errorf("create template stage %q: %w", st.Name, err)
		}
	}
	return tmpl.ID, nil
}

func cleanProcess(in ProcessInput) (ProcessInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" {
		return in, ErrProcessName
	}
	return in, nil
}

func wrapProcess(what string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case errors.Is(err, domain.ErrInvalidPipeline), errors.Is(err, ErrNotFound),
		errors.Is(err, ErrProcessDefault), errors.Is(err, ErrProcessSource), errors.Is(err, ErrProcessName):
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}
