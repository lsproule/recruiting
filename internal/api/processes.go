package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/domain"
	"recruiting/internal/service"
)

// Process is a hiring process: the pipeline template a job is built from.
type Process struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	LibraryKey  string    `json:"library_key,omitempty" doc:"The built-in process this one was copied from"`
	IsDefault   bool      `json:"is_default" doc:"Jobs created without naming a process copy the default"`
	Stages      []Stage   `json:"stages"`
	Jobs        int       `json:"jobs" doc:"How many jobs were built from it"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// LibraryProcess is one built-in process, ready to be copied into the org.
type LibraryProcess struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Stages      []Stage `json:"stages"`
}

func processView(p service.Process) Process {
	return Process{
		ID: p.ID, Name: p.Name, Description: p.Description, LibraryKey: p.LibraryKey, IsDefault: p.IsDefault,
		Stages: stageViews(p.Stages), Jobs: p.Jobs, UpdatedAt: p.UpdatedAt,
	}
}

type processHandlers struct{ d Deps }

func (m *mounter) mountProcesses() {
	h := processHandlers{d: m.d}
	register(m, accessOrg, huma.Operation{
		OperationID: "list-processes", Method: http.MethodGet, Path: "/processes",
		Summary: "The org's hiring processes", Tags: []string{"processes"},
		Description: "Every process the org keeps, the default first. A process is copied into a job at creation; later edits never reach existing jobs.",
	}, h.list)
	register(m, accessOrg, huma.Operation{
		OperationID: "list-process-library", Method: http.MethodGet, Path: "/processes/library",
		Summary: "The built-in processes", Tags: []string{"processes"},
	}, h.library)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-process", Method: http.MethodPost, Path: "/processes",
		Summary: "Add a process", Tags: []string{"processes"}, DefaultStatus: http.StatusCreated,
		Description: "Copies a built-in process (library_key), an existing one (copy_of), or starts from the bare pair of closing stages.",
	}, h.create)
	register(m, accessOrg, huma.Operation{
		OperationID: "get-process", Method: http.MethodGet, Path: "/processes/{process_id}",
		Summary: "One process", Tags: []string{"processes"},
	}, h.get)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-process", Method: http.MethodPut, Path: "/processes/{process_id}",
		Summary: "Rename a process", Tags: []string{"processes"},
	}, h.update)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-process", Method: http.MethodDelete, Path: "/processes/{process_id}",
		Summary: "Delete a process", Tags: []string{"processes"}, DefaultStatus: http.StatusNoContent,
		Description: "The default cannot be deleted. Jobs built from a deleted process keep their stages.",
	}, h.remove)
	register(m, accessOrg, huma.Operation{
		OperationID: "set-default-process", Method: http.MethodPost, Path: "/processes/{process_id}/default",
		Summary: "Make it the default", Tags: []string{"processes"}, DefaultStatus: http.StatusNoContent,
	}, h.setDefault)
	register(m, accessOrg, huma.Operation{
		OperationID: "create-process-stage", Method: http.MethodPost, Path: "/processes/{process_id}/stages",
		Summary: "Append a stage", Tags: []string{"processes"}, DefaultStatus: http.StatusCreated,
	}, h.createStage)
	register(m, accessOrg, huma.Operation{
		OperationID: "update-process-stage", Method: http.MethodPut, Path: "/processes/{process_id}/stages/{stage_id}",
		Summary: "Edit a stage", Tags: []string{"processes"},
	}, h.updateStage)
	register(m, accessOrg, huma.Operation{
		OperationID: "delete-process-stage", Method: http.MethodDelete, Path: "/processes/{process_id}/stages/{stage_id}",
		Summary: "Remove a stage", Tags: []string{"processes"}, DefaultStatus: http.StatusNoContent,
	}, h.deleteStage)
	register(m, accessOrg, huma.Operation{
		OperationID: "reorder-process-stages", Method: http.MethodPost, Path: "/processes/{process_id}/stages/reorder",
		Summary: "Reorder the stages", Tags: []string{"processes"}, DefaultStatus: http.StatusNoContent,
	}, h.reorder)
}

type processesOutput struct {
	Body struct {
		Processes []Process `json:"processes"`
	}
}

type processLibraryOutput struct {
	Body struct {
		Processes []LibraryProcess `json:"processes"`
	}
}

type processOutput struct{ Body Process }

type processInput struct {
	ProcessID uuid.UUID `path:"process_id"`
}

type processStageInput struct {
	ProcessID uuid.UUID `path:"process_id"`
	StageID   uuid.UUID `path:"stage_id"`
}

type createProcessInput struct {
	Body struct {
		Name        string     `json:"name,omitempty" doc:"Defaults to the copied process's name"`
		Description string     `json:"description,omitempty"`
		LibraryKey  string     `json:"library_key,omitempty" doc:"A key from the library listing"`
		CopyOf      *uuid.UUID `json:"copy_of,omitempty" doc:"An existing process to copy"`
	}
}

type updateProcessInput struct {
	ProcessID uuid.UUID `path:"process_id"`
	Body      struct {
		Name        string `json:"name" minLength:"1"`
		Description string `json:"description,omitempty"`
	}
}

type createProcessStageInput struct {
	ProcessID uuid.UUID `path:"process_id"`
	Body      StageInput
}

type updateProcessStageInput struct {
	ProcessID uuid.UUID `path:"process_id"`
	StageID   uuid.UUID `path:"stage_id"`
	Body      StageInput
}

type reorderProcessInput struct {
	ProcessID uuid.UUID `path:"process_id"`
	Body      struct {
		Order []uuid.UUID `json:"order" minItems:"1" doc:"Stage ids in their new order"`
	}
}

func (h processHandlers) list(ctx context.Context, _ *struct{}) (*processesOutput, error) {
	procs, err := h.d.Processes.List(ctx, principal(ctx))
	if err != nil {
		return nil, problemDetail(err)
	}
	out := &processesOutput{}
	out.Body.Processes = make([]Process, 0, len(procs))
	for _, p := range procs {
		out.Body.Processes = append(out.Body.Processes, processView(p))
	}
	return out, nil
}

func (h processHandlers) library(_ context.Context, _ *struct{}) (*processLibraryOutput, error) {
	out := &processLibraryOutput{}
	out.Body.Processes = make([]LibraryProcess, 0, len(domain.ProcessLibrary))
	for _, spec := range domain.ProcessLibrary {
		out.Body.Processes = append(out.Body.Processes, LibraryProcess{
			Key: spec.Key, Name: spec.Name, Description: spec.Description, Stages: stageViews(spec.PipelineStages()),
		})
	}
	return out, nil
}

func (h processHandlers) create(ctx context.Context, in *createProcessInput) (*processOutput, error) {
	name := in.Body.Name
	if name == "" && in.Body.LibraryKey != "" {
		if spec, ok := domain.ProcessByKey(in.Body.LibraryKey); ok {
			name = spec.Name
		}
	}
	from := service.ProcessSource{LibraryKey: in.Body.LibraryKey}
	if in.Body.CopyOf != nil {
		from.ProcessID = *in.Body.CopyOf
		if name == "" {
			src, err := h.d.Processes.Get(ctx, principal(ctx), *in.Body.CopyOf)
			if err != nil {
				return nil, problemDetail(err)
			}
			name = src.Name + " (copy)"
		}
	}
	proc, err := h.d.Processes.Create(ctx, principal(ctx), service.ProcessInput{Name: name, Description: in.Body.Description}, from)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &processOutput{Body: processView(proc)}, nil
}

func (h processHandlers) get(ctx context.Context, in *processInput) (*processOutput, error) {
	proc, err := h.d.Processes.Get(ctx, principal(ctx), in.ProcessID)
	if err != nil {
		return nil, problemDetail(err)
	}
	return &processOutput{Body: processView(proc)}, nil
}

func (h processHandlers) update(ctx context.Context, in *updateProcessInput) (*processOutput, error) {
	proc, err := h.d.Processes.Update(ctx, principal(ctx), in.ProcessID, service.ProcessInput{Name: in.Body.Name, Description: in.Body.Description})
	if err != nil {
		return nil, problemDetail(err)
	}
	return &processOutput{Body: processView(proc)}, nil
}

func (h processHandlers) remove(ctx context.Context, in *processInput) (*struct{}, error) {
	if err := h.d.Processes.Delete(ctx, principal(ctx), in.ProcessID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h processHandlers) setDefault(ctx context.Context, in *processInput) (*struct{}, error) {
	if err := h.d.Processes.SetDefault(ctx, principal(ctx), in.ProcessID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h processHandlers) createStage(ctx context.Context, in *createProcessStageInput) (*stageOutput, error) {
	s, err := h.d.Processes.AddStage(ctx, principal(ctx), in.ProcessID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &stageOutput{Body: stageView(s)}, nil
}

func (h processHandlers) updateStage(ctx context.Context, in *updateProcessStageInput) (*stageOutput, error) {
	s, err := h.d.Processes.UpdateStage(ctx, principal(ctx), in.ProcessID, in.StageID, in.Body.service())
	if err != nil {
		return nil, problemDetail(err)
	}
	return &stageOutput{Body: stageView(s)}, nil
}

func (h processHandlers) deleteStage(ctx context.Context, in *processStageInput) (*struct{}, error) {
	if err := h.d.Processes.DeleteStage(ctx, principal(ctx), in.ProcessID, in.StageID); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}

func (h processHandlers) reorder(ctx context.Context, in *reorderProcessInput) (*struct{}, error) {
	if err := h.d.Processes.ReorderStages(ctx, principal(ctx), in.ProcessID, in.Body.Order); err != nil {
		return nil, problemDetail(err)
	}
	return nil, nil
}
