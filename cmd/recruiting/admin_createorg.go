package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"recruiting/internal/config"
	"recruiting/internal/service"
	"recruiting/internal/store"
	"recruiting/internal/store/system"
)

// createOrgTimeout bounds the whole bootstrap, connection included.
const createOrgTimeout = 30 * time.Second

// createOrg bootstraps an org and its first admin, printing a one-time link
// the admin uses to choose a password. It connects as the schema owner: the
// org does not exist yet, so there is no tenant scope for RLS to apply.
func createOrg(out io.Writer, logger *slog.Logger, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("admin create-org", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "organisation name (required)")
	adminEmail := fs.String("admin-email", "", "email of the first admin user (required)")
	adminName := fs.String("admin-name", "", "display name of the first admin user (defaults to the email)")
	slug := fs.String("slug", "", "URL slug (defaults to a slug of --name)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("admin create-org: %w", err)
	}
	switch {
	case *name == "":
		return errors.New("admin create-org: --name is required")
	case *adminEmail == "":
		return errors.New("admin create-org: --admin-email is required")
	}
	if *slug == "" {
		*slug = orgSlug(*name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), createOrgTimeout)
	defer cancel()
	sys, err := system.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer sys.Close()

	var res service.BootstrapResult
	err = sys.WithSystemTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		var err error
		res, err = service.BootstrapOrg(ctx, tx, service.NewOrg{
			Name: *name, Slug: *slug, AdminEmail: *adminEmail, AdminName: *adminName,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("admin create-org: %w", err)
	}

	logger.Info("org created", "org_id", res.OrgID, "slug", *slug, "admin_user_id", res.AdminUserID)
	fmt.Fprintf(out, "Created org %q (%s), slug %q.\n", *name, res.OrgID, *slug)
	fmt.Fprintf(out, "Admin %s must set a password with this one-time link (valid until %s):\n\n  %s\n\n",
		*adminEmail, res.PasswordSetExpiry.UTC().Format(time.RFC3339), passwordSetLink(cfg.BaseURL, res.PasswordSetToken))
	return nil
}
