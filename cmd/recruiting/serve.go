package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"recruiting/internal/api"
	"recruiting/internal/blob"
	"recruiting/internal/config"
	"recruiting/internal/mail"
	"recruiting/internal/observe"
	"recruiting/internal/queue"
	runnerclient "recruiting/internal/runner/client"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/web/admin"
	applyweb "recruiting/internal/web/apply"
	"recruiting/internal/web/assess"
	"recruiting/internal/web/auth"
	"recruiting/internal/web/availability"
	"recruiting/internal/web/book"
	candidatesweb "recruiting/internal/web/candidates"
	clientweb "recruiting/internal/web/client"
	clientsweb "recruiting/internal/web/clients"
	"recruiting/internal/web/intake"
	"recruiting/internal/web/interviews"
	"recruiting/internal/web/jobs"
	"recruiting/internal/web/layout"
	"recruiting/internal/web/pipeline"
	"recruiting/internal/web/pool"
	"recruiting/internal/web/problems"
	"recruiting/internal/web/processes"
	"recruiting/internal/web/reviews"
	"recruiting/internal/web/room"
	"recruiting/internal/web/scorecards"
	"recruiting/internal/web/shortlist"
	"recruiting/internal/web/sprints"
	talentweb "recruiting/internal/web/talent"
	"recruiting/internal/web/workqueue"
)

// defaultListenAddr is where serve binds when LISTEN_ADDR is unset. The
// address is serve's alone, so it stays out of the shared configuration every
// mode has to satisfy.
const defaultListenAddr = ":8080"

// shutdownGrace bounds how long in-flight requests have once a signal arrives.
const shutdownGrace = 20 * time.Second

// readHeaderTimeout caps how long a client may take to send its headers.
const readHeaderTimeout = 10 * time.Second

func listenAddr() string {
	if v := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); v != "" {
		return v
	}
	return defaultListenAddr
}

// serve opens the tenant store, builds the HTTP surface, and serves it on addr
// until ctx is cancelled. ready, when set, receives the bound address, which
// matters when addr asks the kernel for a port.
func serve(ctx context.Context, logger *slog.Logger, cfg *config.Config, addr string, ready func(net.Addr)) error {
	st, err := store.Open(ctx, cfg.DatabaseURLApp)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	defer st.Close()

	handler, err := appHandler(ctx, logger, cfg, st)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("serve: listen on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "error", err)
		}
	}()

	logger.Info("listening", "addr", ln.Addr().String(), "base_url", cfg.BaseURL)
	if ready != nil {
		ready(ln.Addr())
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	<-stopped
	return nil
}

// appHandler is the composition root of the serve mode: every surface the
// binary exposes is mounted here.
//
// The JSON API and the health check sit on the outer mux so the HTML
// surface's CSRF and session middleware never touch them. Everything under
// the HTML surface goes on a second mux mounted at the root, because
// auth.Mount installs that middleware and chi only accepts middleware before
// the first route. Add new HTML routes after auth.Mount on `web`; add API
// operations to r.API (or, if a resource group is not covered by
// api.MountAll yet, to r.Mux directly).
func appHandler(ctx context.Context, logger *slog.Logger, cfg *config.Config, st *store.Store) (http.Handler, error) {
	blobs, err := objectStore(ctx, logger, cfg)
	if err != nil {
		return nil, err
	}
	// blobStore and blobReader stay true nil interfaces when object storage
	// is not configured, rather than a non-nil interface wrapping a nil
	// *blob.Client — the services behind them tell the two cases apart with
	// a plain nil check.
	var blobStore service.BlobStore
	var blobReader service.BlobReader
	if blobs != nil {
		blobStore = blobs
		blobReader = blobs
	}

	q, err := queue.New(st.Pool(), queue.Config{Logger: logger})
	if err != nil {
		return nil, err
	}

	resumes := service.NewResumeService(st, blobStore)
	resumes.Logger = logger
	candidates := service.NewCandidateService(st, resumes, q)

	r := api.NewRouter()
	layout.MountStatic(r.Mux)
	r.Mux.Handle("/metrics", observe.Handler())

	web := chi.NewMux()
	// The body cap goes on before auth.Mount: CSRF reads the multipart form
	// the upload surfaces post, and would otherwise parse an unbounded body.
	web.Use(applyweb.MaxBody(applyweb.UploadBodyLimit))
	authService := service.NewAuthService(st)
	links := service.NewMagicLinkService(st)
	authSurface, err := auth.Mount(web, auth.Deps{
		Auth:              authService,
		Links:             links,
		BaseURL:           cfg.BaseURL,
		CookieSecret:      []byte(cfg.SessionSecret),
		SendPasswordReset: sendPasswordReset(st, q, cfg.BaseURL),
	})
	if err != nil {
		return nil, err
	}
	// auth.Mount already registered routes directly on `web`, and chi
	// refuses Use() once a mux carries routes — so the rest of the app lives
	// on its own subrouter, mounted at "/". That also gives its request
	// logging middleware both ids on every line: web's CSRF and Authenticate
	// middleware (installed by auth.Mount) run first and set the principal
	// on the request before it ever reaches app's own middleware stack.
	app := chi.NewMux()
	app.Use(observe.RequestLogging(logger))

	org := service.NewOrgService(st)
	jobService := service.NewJobService(st)
	applications := service.NewApplicationService(st, q, cfg.BaseURL)
	releases := service.NewReleaseService(st, q, cfg.BaseURL)
	portal := service.NewClientPortalService(st, applications, resumes, q, cfg.BaseURL)
	schedule := service.NewScheduleService(st, q, cfg.BaseURL)
	poolService := service.NewPoolService(st)
	scorecardService := service.NewScorecardService(st, poolService)
	runnerExec := runnerclient.New(cfg.RunnerURL, cfg.RunnerSecret)
	problemService := service.NewProblemService(st, runnerExec)
	assessmentService := service.NewAssessmentService(st)
	attempts := service.NewAttemptService(st, q, cfg.BaseURL)
	// The webcam frames live in object storage: without it the session
	// cannot store one and the reviewer's timeline opens nothing.
	attempts.Blobs = blobStore
	attempts.Logger = logger
	reviewService := service.NewReviewService(st, poolService, blobReader)
	shortlists := service.NewShortlistService(st, releases, reviewService, poolService, q, cfg.BaseURL)
	apiTokens := service.NewAPITokenService(st)
	clientAccounts := service.NewClientService(st, applications)
	workQueue := service.NewWorkQueueService(st)
	processService := service.NewProcessService(st)
	sprintService := service.NewSprintService(st, q, cfg.BaseURL)
	interviewService := service.NewInterviewService(st)
	roomService := service.NewRoomService(st, runnerExec)
	talentService := service.NewTalentService(st, resumes, links, q, cfg.BaseURL)
	roomDeps := room.Deps{Rooms: roomService, Org: org, ICEServers: cfg.RTCICEServers, Logger: logger}

	// The sidebar's badges are computed once per request, and only when a
	// page actually draws the sidebar.
	app.Use(layout.WithCounts(workQueue.NavCounts, logger))

	admin.Mount(app, admin.Deps{Org: org, BaseURL: cfg.BaseURL, SendInvite: sendPasswordReset(st, q, cfg.BaseURL)})
	mountAPITokens(app, apiTokensDeps{Tokens: apiTokens, Org: org, Logger: logger})
	jobs.Mount(app, jobs.Deps{Jobs: jobService, Org: org, Logger: logger})
	pipeline.Mount(app, pipeline.Deps{
		Applications: applications, Release: releases, Schedule: schedule, Org: org,
		Reviews: reviewService, Attempts: attempts, Pool: poolService, Candidates: candidates,
		Interviews: interviewService, Rooms: roomService, Sprints: sprintService,
		Logger: logger,
	})
	processes.Mount(app, processes.Deps{Processes: processService, Org: org, Logger: logger})
	interviews.Mount(app, interviews.Deps{Interviews: interviewService, Sprints: sprintService, Org: org, Logger: logger})
	room.Mount(app, roomDeps)
	sprints.Mount(app, sprints.Deps{
		Sprints: sprintService, Applications: applications, Jobs: jobService, Org: org, Links: links,
		Room: roomDeps, Logger: logger,
	})
	applyweb.Mount(app, applyweb.Deps{Candidates: candidates, Logger: logger})
	candidatesweb.Mount(app, candidatesweb.Deps{Candidates: candidates, Jobs: jobService, Org: org, Logger: logger})
	availability.Mount(app, availability.Deps{Schedule: schedule, Org: org, Logger: logger})
	app.Group(func(g chi.Router) {
		// Refused bookings (the slot was taken first) are the metric; the
		// wrapper only ever looks at the method and the status book.go
		// already writes for that case, never at the request body.
		g.Use(bookingConflictMetrics)
		book.Mount(g, book.Deps{Schedule: schedule, Links: links, Room: roomDeps, Logger: logger})
	})
	scorecards.Mount(app, scorecards.Deps{Scorecards: scorecardService, Org: org, Logger: logger})
	clientweb.Mount(app, clientweb.Deps{
		Portal: portal, Shortlists: shortlists, Talent: talentService, Tokens: apiTokens, BaseURL: cfg.BaseURL, Logger: logger,
	})
	talentweb.Mount(app, talentweb.Deps{Talent: talentService, Links: links, Logger: logger})
	talentweb.MountApp(app, talentweb.Deps{Talent: talentService, Org: org, Logger: logger})
	// pool.Mount and shortlist.Mount must come after jobs.Mount: their
	// per-job screens share jobs's subrouter.
	pool.Mount(app, pool.Deps{Pool: poolService, Org: org, Logger: logger})
	shortlist.Mount(app, shortlist.Deps{Shortlists: shortlists, Org: org, Logger: logger})
	problems.Mount(app, problems.Deps{Problems: problemService, Org: org, Logger: logger})
	intake.Mount(app, intake.Deps{
		Intake:   service.NewIntakeService(st, q, cfg.BaseURL),
		Problems: problemService, Org: org, Logger: logger,
	})
	assess.Mount(app, assess.Deps{Attempts: attempts, Assessment: authSurface.Assessment(), API: r.Mux, Logger: logger})
	assess.MountRecruiter(app, assess.RecruiterDeps{Assessments: assessmentService, Problems: problemService, Jobs: jobService, Attempts: attempts, Org: org, Logger: logger})
	reviews.Mount(app, reviews.Deps{Reviews: reviewService, Org: org, Attempts: attempts, Logger: logger})
	workqueue.Mount(app, workqueue.Deps{Queue: workQueue, Org: org, Logger: logger})
	// clients.Mount must come after intake.Mount, which owns /app/clients/new.
	clientsweb.Mount(app, clientsweb.Deps{Clients: clientAccounts, Org: org, Logger: logger})

	web.Mount("/", app)
	r.Mux.Mount("/", web)

	api.MountAll(r, api.Deps{
		Sessions:     authService,
		Tokens:       apiTokens,
		Links:        links,
		Jobs:         jobService,
		Applications: applications,
		Releases:     releases,
		Candidates:   candidates,
		Resumes:      resumes,
		Scorecards:   scorecardService,
		Schedule:     schedule,
		Problems:     problemService,
		Assessments:  assessmentService,
		Attempts:     attempts,
		Reviews:      reviewService,
		Pool:         poolService,
		Portal:       portal,
		Shortlists:   shortlists,
		APITokens:    apiTokens,
		Processes:    processService,
		Sprints:      sprintService,
		Rooms:        roomService,
		Talent:       talentService,
	})

	go observe.PollQueueDepth(ctx, st.Pool(), queueDepthPollInterval, logger)

	return r.Mux, nil
}

// queueDepthPollInterval is how often the queue-depth gauge refreshes from
// river_job. Scrapes land between polls, not on them, so it only needs to be
// short next to a typical scrape interval, not instantaneous.
const queueDepthPollInterval = 15 * time.Second

// objectStore builds the resume/recording bucket. Configuration is required
// of a deployment, but an embedding that leaves it out — a test of the HTML
// surface, say — still gets a server; the upload paths then refuse with
// service.ErrNoBlobStore rather than the process failing to start.
func objectStore(ctx context.Context, logger *slog.Logger, cfg *config.Config) (*blob.Client, error) {
	client, err := blob.New(blob.Config{
		Endpoint: cfg.BlobEndpoint,
		Bucket:   cfg.BlobBucket,
		Key:      cfg.BlobKey,
		Secret:   cfg.BlobSecret,
	})
	if errors.Is(err, blob.ErrNotConfigured) {
		logger.Warn("object storage is not configured; resume upload and download are disabled")
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := client.EnsureBucket(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

// resetToken reads the token out of a reset link, which is its last path
// segment. Parsing rather than slicing keeps a query string or a trailing
// slash from being taken for part of the token.
func resetToken(link string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("password reset link: %w", err)
	}
	token := path.Base(strings.TrimRight(u.Path, "/"))
	if token == "" || token == "." || token == "/" {
		return "", fmt.Errorf("password reset link %q carries no token", link)
	}
	return token, nil
}

// sendPasswordReset queues the reset email. The reset surface knows only the
// address and the link, and email_log is a tenant table, so the org comes
// from the reset row the link's token addresses — the one lookup that works
// before any principal exists.
//
// The link goes into the job payload, which means a live reset URL sits in
// the queue until the job is worked and then cleaned up; queue.CompletedRetention
// is short for exactly this reason.
func sendPasswordReset(st *store.Store, q *queue.Client, baseURL string) func(context.Context, string, string) error {
	return func(ctx context.Context, email, link string) error {
		token, err := resetToken(link)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(token))
		var orgID uuid.UUID
		if err := st.WithLookupTx(ctx, store.Lookup{TokenHash: hex.EncodeToString(sum[:])}, func(ctx context.Context, tx *store.Tx) error {
			reset, err := tx.Q.LookupPasswordReset(ctx, hex.EncodeToString(sum[:]))
			if err != nil {
				return fmt.Errorf("password reset lookup: %w", err)
			}
			orgID = reset.OrgID
			return nil
		}); err != nil {
			return err
		}
		p := service.Principal{Kind: service.PrincipalSystem, OrgID: orgID}
		return st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
			_, err := q.Enqueue(ctx, tx, queue.KindEmailSend, queue.EmailPayload{
				Template: mail.TemplatePasswordReset,
				To:       email,
				OrgID:    orgID,
				Data:     map[string]any{"ResetURL": link, "BaseURL": baseURL},
			})
			return err
		})
	}
}
