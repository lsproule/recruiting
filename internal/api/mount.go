package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"recruiting/internal/service"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/middleware"
)

// TokenResolver turns a bearer secret into its principal;
// service.APITokenService satisfies it.
type TokenResolver interface {
	ResolveToken(ctx context.Context, raw string) (service.Principal, error)
}

// Deps is every collaborator the /api/v1 operations need. The operations are
// thin: each one wraps a service method, so the surface owns no rules of its
// own beyond who may reach which operation.
//
// Sessions and Tokens are the two credentials the API accepts. Resolve
// overrides both, which is what the tests and an embedding that already has a
// principal on the request use.
type Deps struct {
	Sessions middleware.SessionResolver
	Tokens   TokenResolver

	Links        *service.MagicLinkService
	Jobs         *service.JobService
	Applications *service.ApplicationService
	Releases     *service.ReleaseService
	Candidates   *service.CandidateService
	Resumes      *service.ResumeService
	Scorecards   *service.ScorecardService
	Schedule     *service.ScheduleService
	Problems     *service.ProblemService
	Assessments  *service.AssessmentService
	Attempts     *service.AttemptService
	Reviews      *service.ReviewService
	Pool         *service.PoolService
	Portal       *service.ClientPortalService
	APITokens    *service.APITokenService

	// Resolve replaces the session-or-bearer resolution for every operation
	// but the candidate's attempt routes and the booking link.
	Resolve func(*http.Request) (service.Principal, bool)
	// AttemptResolve resolves the sealed assessment cookie the candidate's
	// attempt operations authenticate with; nil reads the request context.
	AttemptResolve func(*http.Request) (service.Principal, bool)
}

// BearerScheme is the name of the security scheme the operations declare. A
// browser island authenticates with the session cookie instead, which the
// document does not describe: it is not a credential a client presents.
const BearerScheme = "bearer"

// bearerSecurity is what a guarded operation declares. Operations
// authenticated by a token in the path declare no scheme at all.
var bearerSecurity = []map[string][]string{{BearerScheme: {}}}

// selfGuardedOps are the operations that carry their own credential and their
// own middleware: the candidate's sealed assessment cookie, and the
// reviewer's replay manifest. Naming them keeps the guard default-deny, so an
// operation merely forgotten in a mount is refused rather than served.
var selfGuardedOps = map[string]bool{
	"get-attempt":            true,
	"record-attempt-events":  true,
	"save-attempt-source":    true,
	"run-attempt-problem":    true,
	"submit-attempt-problem": true,
	"get-attempt-submission": true,
	"finish-attempt":         true,
	replayOp:                 true,
}

// access is who may reach an operation.
type access int

const (
	// accessOrg is any signed-in org user; the service enforces the role the
	// method itself needs.
	accessOrg access = iota
	// accessAdmin is an org user holding the admin role.
	accessAdmin
	// accessPortal is a client user, and is the only access a client-user
	// credential is ever granted.
	accessPortal
	// accessLink is a candidate's booking link token, carried in the path.
	accessLink
)

func (a access) allows(p service.Principal) bool {
	switch a {
	case accessOrg:
		return p.Kind == service.PrincipalOrgUser
	case accessAdmin:
		return p.Kind == service.PrincipalOrgUser && p.HasRole("admin")
	case accessPortal:
		return p.Kind == service.PrincipalClientUser
	case accessLink:
		return p.Kind == service.PrincipalMagicLink && p.MagicPurpose == service.LinkBook
	}
	return false
}

// guarded is one registered operation and the access it demands.
type guarded struct {
	path   string
	access access
}

// mounter registers operations and remembers what each one demands, which is
// what the authentication middleware consults; huma middleware runs for every
// operation, including the ones mounted elsewhere.
type mounter struct {
	api huma.API
	d   Deps
	ops map[string]guarded
}

// register adds one operation under m's authentication.
func register[I, O any](m *mounter, a access, op huma.Operation, h func(context.Context, *I) (*O, error)) {
	m.ops[op.OperationID] = guarded{path: op.Path, access: a}
	if a == accessLink {
		// The booking link token in the path is the whole credential.
		op.Security = []map[string][]string{}
	} else {
		op.Security = bearerSecurity
	}
	huma.Register(m.api, op, h)
}

// MountAll registers every /api/v1 resource group on r, including the
// candidate attempt operations and the reviewer's replay manifest.
func MountAll(r *Router, d Deps) { mountAll(r.API, d) }

func mountAll(a huma.API, d Deps) map[string]guarded {
	m := &mounter{api: a, d: d, ops: map[string]guarded{}}
	m.authenticate()

	m.mountJobs()
	m.mountStages()
	m.mountApplications()
	m.mountCandidates()
	m.mountScorecards()
	m.mountSchedule()
	m.mountProblems()
	m.mountAssessments()
	m.mountReviews()
	m.mountPool()
	m.mountPortal()
	m.mountAPITokens()

	MountAttempts(a, AttemptsDeps{Attempts: d.Attempts, Resolve: d.AttemptResolve})
	MountReplay(a, ReplayDeps{Reviews: d.Reviews, Resolve: d.resolve()})
	return m.ops
}

// resolve is how an operation of the org or portal surface finds its caller:
// the HTML surface's own session middleware first, then a bearer token.
func (d Deps) resolve() func(*http.Request) (service.Principal, bool) {
	sessionAware := d.resolveWithSource()
	return func(r *http.Request) (service.Principal, bool) {
		p, _, ok := sessionAware(r)
		return p, ok
	}
}

// resolveWithSource is resolve that also reports whether the principal came
// from the session cookie, which is what decides whether an unsafe request
// must carry the CSRF token: a browser sends cookies on its own, a bearer
// token only a client that holds it can present.
func (d Deps) resolveWithSource() func(*http.Request) (p service.Principal, fromSession, ok bool) {
	if d.Resolve != nil {
		return func(r *http.Request) (service.Principal, bool, bool) {
			p, ok := d.Resolve(r)
			return p, false, ok
		}
	}
	var session func(*http.Request) (service.Principal, bool)
	if d.Sessions != nil {
		session = ResolveWith(middleware.Authenticate(d.Sessions))
	}
	return func(r *http.Request) (service.Principal, bool, bool) {
		if session != nil {
			if p, ok := session(r); ok {
				return p, true, true
			}
		}
		raw := bearer(r)
		if raw == "" || d.Tokens == nil {
			return service.Principal{}, false, false
		}
		p, err := d.Tokens.ResolveToken(r.Context(), raw)
		if err != nil {
			return service.Principal{}, false, false
		}
		return p, false, true
	}
}

// safeMethod reports whether the method cannot change state and so needs no
// CSRF token.
func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) < len("Bearer ") || !strings.EqualFold(h[:len("Bearer ")], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[len("Bearer "):])
}

// authenticate resolves the caller once per request and refuses anyone the
// operation does not admit. A session-cookie caller making an unsafe request
// must also present the CSRF double-submit token, since the browser sends
// the cookie whether or not the user meant the request; a bearer token is
// exempt. Operations mounted elsewhere carry their own credential and are
// passed through untouched.
func (m *mounter) authenticate() {
	resolve := m.d.resolveWithSource()
	m.api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		id := ctx.Operation().OperationID
		g, ok := m.ops[id]
		if !ok {
			if selfGuardedOps[id] {
				next(ctx)
				return
			}
			_ = huma.WriteErr(m.api, ctx, http.StatusUnauthorized, "this operation has no credential guard")
			return
		}
		r, _ := humachi.Unwrap(ctx)
		var p service.Principal
		var found, fromSession bool
		if g.access == accessLink {
			p, found = m.link(r)
		} else {
			p, fromSession, found = resolve(r)
		}
		if !found {
			_ = huma.WriteErr(m.api, ctx, http.StatusUnauthorized, "sign in or present an API token")
			return
		}
		if fromSession && !safeMethod(r.Method) && !middleware.CheckCSRF(r) {
			_ = huma.WriteErr(m.api, ctx, http.StatusForbidden, "invalid or missing CSRF token")
			return
		}
		if !g.access.allows(p) {
			_ = huma.WriteErr(m.api, ctx, http.StatusForbidden, "this credential cannot use this operation")
			return
		}
		next(huma.WithValue(ctx, principalKey{}, p))
	})
}

// link resolves the booking token in the path through the same middleware the
// booking page uses, so a read resolves the link and a write consumes it by
// exactly the rules that surface follows.
func (m *mounter) link(r *http.Request) (service.Principal, bool) {
	if m.d.Links == nil {
		return service.Principal{}, false
	}
	return ResolveWith(auth.MagicLink(m.d.Links, service.LinkBook))(r)
}
