package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"recruiting/internal/api"
)

// resource is one resource the specification requires v1 to expose, and the
// operation ids that serve its collection, its member, and its creation. An
// empty verb must be explained by Why: not every resource is created through
// an operation of its own.
type resource struct {
	Name   string
	List   string
	Get    string
	Create string
	Extra  []string
	Why    string
}

// specResources mirrors the API section of the specification: jobs,
// applications (+ move), candidates, resumes, stages, scorecards,
// availability + slots + bookings, problems (+ import), assessments,
// attempts (+ events, runs, submits, replay), reviews, talent pool
// (+ suggestions), and the client-portal reads.
var specResources = []resource{
	{
		Name: "jobs", List: "list-jobs", Get: "get-job", Create: "create-job",
		Extra: []string{"update-job", "get-job-board"},
	},
	{
		Name: "stages", List: "list-stages", Get: "get-stage", Create: "create-stage",
		Extra: []string{"update-stage", "delete-stage", "reorder-stages"},
	},
	{
		Name: "applications", List: "list-applications", Get: "get-application",
		Extra: []string{"move-application", "withdraw-application", "release-application", "unrelease-application", "list-application-events"},
		Why:   "an application is created by an apply submission or by adding a candidate to a job, not by a create of its own",
	},
	{
		Name: "candidates", List: "list-candidates", Get: "get-candidate", Create: "create-candidate",
	},
	{
		Name: "resumes", List: "list-resumes", Get: "get-resume",
		Why: "a resume is uploaded with the candidate that owns it, so create-candidate carries the file",
	},
	{
		Name: "scorecards", List: "list-scorecards", Get: "get-scorecard", Create: "create-scorecard",
		Extra: []string{"list-scorecard-assignments", "get-rubric", "set-rubric"},
	},
	{
		Name: "availability", Get: "get-availability",
		Extra: []string{"set-availability", "create-availability-exception", "delete-availability-exception"},
		Why:   "availability is one record per vetter: it is read and replaced, never listed or created",
	},
	{
		Name: "slots", List: "list-slots",
		Extra: []string{"set-slot-outcome", "assign-vetter"},
		Why:   "slots are generated from availability and booked through a booking; there is no slot member or slot create",
	},
	{
		Name: "bookings", Get: "get-booking", Create: "create-booking",
		Extra: []string{"cancel-booking"},
		Why:   "a booking is addressed by the candidate's link token, which lists nothing beyond that one booking",
	},
	{
		Name: "problems", List: "list-problems", Get: "get-problem", Create: "create-problem",
		Extra: []string{"update-problem", "delete-problem", "import-problems", "try-problem", "clone-problem"},
	},
	{
		Name: "assessments", List: "list-assessments", Get: "get-assessment", Create: "create-assessment",
		Extra: []string{"update-assessment", "delete-assessment", "attach-stage-assessment", "get-stage-assessment"},
	},
	{
		Name: "attempts", Get: "get-attempt",
		Extra: []string{"record-attempt-events", "run-attempt-problem", "submit-attempt-problem", "get-attempt-submission", "save-attempt-source", "finish-attempt", "get-attempt-replay",
			"upload-attempt-snapshot", "upload-attempt-identity"},
		Why: "an attempt is created when an application enters an assessment stage; the candidate never lists or creates one",
	},
	{
		Name: "reviews", List: "list-review-assignments", Get: "get-review", Create: "create-review",
		Extra: []string{"list-application-reviews"},
	},
	{
		Name: "pool", List: "list-pool-entries", Get: "get-pool-entry", Create: "create-pool-entry",
		Extra: []string{"update-pool-entry", "delete-pool-entry", "list-pool-suggestions", "add-pool-entry-to-job"},
	},
	{
		Name: "shortlists", Get: "get-shortlist", Create: "create-shortlist",
		Extra: []string{"send-shortlist"},
		Why:   "packets are read per job on the builder screen; the API addresses one packet at a time",
	},
	{
		Name: "client-portal", List: "list-portal-jobs", Get: "get-portal-job",
		Extra: []string{"get-portal-me", "get-portal-shortlist", "list-portal-applications", "get-portal-application", "get-portal-resume",
			"advance-portal-application", "reject-portal-application", "request-portal-info", "list-portal-events",
			"list-portal-tokens", "create-portal-token", "revoke-portal-token"},
		Why: "a company creates nothing here but its own tokens and talent requests; jobs and applications are the recruiter's to open",
	},
	{
		Name: "talent-network", List: "list-portal-talent-requests", Get: "get-portal-talent-request", Create: "create-portal-talent-request",
		Extra: []string{"close-portal-talent-request", "list-portal-talent-matches", "introduce-portal-talent-match",
			"list-talent-requests", "get-talent-request", "send-talent-opportunity", "dismiss-talent-introduction",
			"list-talent-profiles", "get-talent-profile"},
	},
	{
		Name: "processes", List: "list-processes", Get: "get-process", Create: "create-process",
		Extra: []string{"update-process", "delete-process", "set-default-process", "list-process-library",
			"create-process-stage", "update-process-stage", "delete-process-stage", "reorder-process-stages"},
	},
	{
		Name: "sprints", List: "list-sprints", Get: "get-sprint", Create: "create-sprint",
		Extra: []string{"update-sprint", "schedule-sprint", "start-sprint", "cancel-sprint", "rate-sprint-pairing", "get-sprint-summary"},
	},
	{
		Name: "rooms", Get: "get-room-code",
		Why: "a room is joined from a page with a browser session; the API only reads what its editor holds",
	},
	{
		Name: "api-tokens", List: "list-api-tokens", Create: "create-api-token",
		Extra: []string{"revoke-api-token"},
		Why:   "a token's secret is shown once at creation and never read back, so there is no member read",
	},
}

// document is the operation ids the served OpenAPI document carries, by id.
func document(t *testing.T) map[string]string {
	t.Helper()
	r := api.NewRouter()
	api.MountAll(r, api.Deps{})
	srv := httptest.NewServer(r.Mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get openapi.json: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse document: %v", err)
	}
	ops := make(map[string]string)
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if op.OperationID != "" {
				ops[op.OperationID] = method + " " + path
			}
		}
	}
	if len(ops) == 0 {
		t.Fatal("the served document carries no operations")
	}
	return ops
}

func TestOpenAPICoversEverySpecResource(t *testing.T) {
	ops := document(t)
	for _, res := range specResources {
		t.Run(res.Name, func(t *testing.T) {
			verbs := map[string]string{"list": res.List, "get": res.Get, "create": res.Create}
			for verb, id := range verbs {
				if id == "" {
					if res.Why == "" {
						t.Errorf("%s has no %s operation and no reason why it does not apply", res.Name, verb)
					}
					continue
				}
				if _, ok := ops[id]; !ok {
					t.Errorf("%s %s: operation %q is not in the document", res.Name, verb, id)
				}
			}
			for _, id := range res.Extra {
				if _, ok := ops[id]; !ok {
					t.Errorf("%s: operation %q is not in the document", res.Name, id)
				}
			}
		})
	}
}

// TestOpenAPIHasNoUndeclaredOperations keeps the document and the resource
// table from drifting apart in the other direction.
func TestOpenAPIHasNoUndeclaredOperations(t *testing.T) {
	declared := map[string]bool{}
	for _, res := range specResources {
		for _, id := range append([]string{res.List, res.Get, res.Create}, res.Extra...) {
			if id != "" {
				declared[id] = true
			}
		}
	}
	for id, route := range document(t) {
		if !declared[id] {
			t.Errorf("operation %q (%s) is served but belongs to no spec resource", id, route)
		}
	}
}
