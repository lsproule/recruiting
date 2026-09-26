package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"recruiting/internal/config"
	"recruiting/internal/demo"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
)

// seedDemoTimeout bounds writing the whole demo, résumé uploads included.
const seedDemoTimeout = 3 * time.Minute

// seedDemo fills a fresh org with a month of invented agency work so every
// screen has something on it. It connects as the schema owner, like
// create-org, because it creates the org; résumés go to object storage when
// it is configured. Run seed-problems first so the assessments carry the
// platform bank; without it the demo makes do with three problems of its own.
func seedDemo(out io.Writer, logger *slog.Logger, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("seed-demo", flag.ContinueOnError)
	fs.SetOutput(out)
	name := fs.String("name", "Northwind Talent", "the demo agency's name; its slug is derived from it")
	adminEmail := fs.String("admin-email", "", "the first admin's sign-in (default admin@<slug>.example)")
	password := fs.String("password", "demo-password", "the password every demo account signs in with")
	candidates := fs.Int("candidates", 48, "how many people apply or join the network")
	seed := fs.Int64("seed", 0, "random seed; the same seed writes the same demo")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), seedDemoTimeout)
	defer cancel()

	var blob service.BlobStore
	if client, err := objectStore(ctx, logger, cfg); err != nil {
		return fmt.Errorf("admin seed-demo: %w", err)
	} else if client != nil {
		blob = client
	}
	sys, err := system.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer sys.Close()

	var report demo.Report
	err = sys.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		report, err = demo.Seed(ctx, tx, demo.Options{
			OrgName: *name, AdminEmail: *adminEmail, Password: *password, Candidates: *candidates, Seed: *seed, Blob: blob,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("admin seed-demo: %w", err)
	}
	logger.Info("demo org seeded", "org", report.OrgSlug, "counts", report.Counts)
	fmt.Fprint(out, demoSummary(cfg.BaseURL, report))
	return nil
}

// demoSummary is what the command prints: where to sign in, as whom, and
// what is there.
func demoSummary(baseURL string, r demo.Report) string {
	var b strings.Builder
	base := strings.TrimRight(baseURL, "/")
	fmt.Fprintf(&b, "Seeded the demo org %q.\n\n", r.OrgSlug)
	keys := make([]string, 0, len(r.Counts))
	for k := range r.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-18s %d\n", k, r.Counts[k])
	}
	fmt.Fprintf(&b, "\nSign in at %s/app/login (recruiters) or %s/client/login (clients):\n\n", base, base)
	for _, l := range r.Logins {
		who := strings.Join(l.Roles, ", ")
		if l.Kind == "client" {
			who = "client · " + l.Company
		}
		fmt.Fprintf(&b, "  %-32s %-24s %s\n", l.Email, l.Password, who)
	}
	fmt.Fprintf(&b, "\nCandidates apply at %s%s/%s/<job-slug> and join the network at %s/talent/%s.\n", base, "/apply", r.OrgSlug, base, r.OrgSlug)
	return b.String()
}
