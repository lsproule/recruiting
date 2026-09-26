package api

import (
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"recruiting/internal/service"
)

// TestPortalAccessIsExactlyTheClientReads pins the token scoping rule: a
// client user's credential reaches the /portal operations and nothing else.
func TestPortalAccessIsExactlyTheClientReads(t *testing.T) {
	r := NewRouter()
	ops := mountAll(r.API, Deps{})
	if len(ops) == 0 {
		t.Fatal("no operations were registered")
	}
	client := service.Principal{Kind: service.PrincipalClientUser, OrgID: uuid.New(), ClientCompanyID: uuid.New()}
	org := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), Roles: []string{"recruiter"}}
	admin := service.Principal{Kind: service.PrincipalOrgUser, OrgID: uuid.New(), Roles: []string{"admin"}}

	for id, op := range ops {
		portal := strings.HasPrefix(op.path, "/portal/")
		switch {
		case portal && op.access != accessPortal:
			t.Errorf("%s (%s) is a portal read but is not client-scoped", id, op.path)
		case !portal && op.access == accessPortal:
			t.Errorf("%s (%s) is client-scoped but is not a portal read", id, op.path)
		}
		if op.access.allows(client) != portal {
			t.Errorf("%s: client reachable = %v, want %v", id, op.access.allows(client), portal)
		}
		if op.access == accessAdmin {
			if op.access.allows(org) {
				t.Errorf("%s: a recruiter must not reach an admin operation", id)
			}
			if !op.access.allows(admin) {
				t.Errorf("%s: an admin must reach an admin operation", id)
			}
		}
	}
}

// TestMagicLinkOperationsAreTheBookingOnes keeps the link-token credential
// from spreading beyond the candidate's own booking.
func TestMagicLinkOperationsAreTheBookingOnes(t *testing.T) {
	r := NewRouter()
	for id, op := range mountAll(r.API, Deps{}) {
		if (op.access == accessLink) != strings.HasPrefix(op.path, "/bookings/") {
			t.Errorf("%s (%s): link access = %v", id, op.path, op.access == accessLink)
		}
	}
}

// documentOps is every operation the mounted document serves, by id, with the
// method and path it answers on.
func documentOps(t *testing.T, r *Router) map[string]string {
	t.Helper()
	out := map[string]string{}
	for path, item := range r.API.OpenAPI().Paths {
		for method, op := range map[string]*huma.Operation{
			"GET": item.Get, "POST": item.Post, "PUT": item.Put,
			"DELETE": item.Delete, "PATCH": item.Patch, "HEAD": item.Head,
		} {
			if op == nil {
				continue
			}
			if prev, dup := out[op.OperationID]; dup {
				t.Errorf("operation id %q is used twice: %s and %s %s", op.OperationID, prev, method, path)
			}
			out[op.OperationID] = method + " " + path
		}
	}
	return out
}

// TestEveryServedOperationIsGuarded is the default-deny check: an operation
// the guard map does not cover, and that is not one of the self-guarded ones,
// would be refused at runtime, so it must not exist.
func TestEveryServedOperationIsGuarded(t *testing.T) {
	r := NewRouter()
	ops := mountAll(r.API, Deps{})
	served := documentOps(t, r)

	for id, route := range served {
		_, guardedHere := ops[id]
		if !guardedHere && !selfGuardedOps[id] {
			t.Errorf("%s (%s) is served but neither guarded nor self-guarded", id, route)
		}
		if guardedHere && selfGuardedOps[id] {
			t.Errorf("%s is both guarded and listed as self-guarded", id)
		}
	}
	for id := range ops {
		if _, ok := served[id]; !ok {
			t.Errorf("%s is guarded but is not served", id)
		}
	}
	for id := range selfGuardedOps {
		if _, ok := served[id]; !ok {
			t.Errorf("%s is exempt from the guard but is not served; drop the exemption", id)
		}
	}
}

// TestGuardedOperationsDeclareTheBearerScheme keeps the document honest about
// the credential a client presents. A booking operation declares none: its
// credential is the link token in the path.
func TestGuardedOperationsDeclareTheBearerScheme(t *testing.T) {
	r := NewRouter()
	ops := mountAll(r.API, Deps{})
	if r.API.OpenAPI().Components.SecuritySchemes[BearerScheme] == nil {
		t.Fatalf("the document declares no %q security scheme", BearerScheme)
	}
	for path, item := range r.API.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch, item.Head} {
			if op == nil {
				continue
			}
			g, ok := ops[op.OperationID]
			if !ok {
				continue
			}
			declared := false
			for _, req := range op.Security {
				if _, named := req[BearerScheme]; named {
					declared = true
				}
			}
			if want := g.access != accessLink; declared != want {
				t.Errorf("%s (%s): declares bearer = %v, want %v", op.OperationID, path, declared, want)
			}
		}
	}
}
