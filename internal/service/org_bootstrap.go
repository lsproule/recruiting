package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// PasswordSetTTL is how long a bootstrap admin has to choose a password.
const PasswordSetTTL = 7 * 24 * time.Hour

// DefaultPipelineTemplateName names the pipeline every new org starts with.
const DefaultPipelineTemplateName = "Default"

// StageSpec is one stage of a pipeline template.
type StageSpec struct {
	Name    string
	Kind    string
	Unblind bool
}

// DefaultPipelineStages is the stage sequence seeded for a new org; jobs are
// created from it until the org edits its templates.
var DefaultPipelineStages = []StageSpec{
	{Name: "Applied", Kind: "generic"},
	{Name: "Screened", Kind: "generic"},
	{Name: "Phone Interview", Kind: "interview"},
	{Name: "Assessment", Kind: "assessment"},
	{Name: "Shortlist", Kind: "generic"},
	{Name: "Client Review", Kind: "client_review"},
	{Name: "Client Interview", Kind: "client_review", Unblind: true},
	{Name: "Offer", Kind: "client_review"},
	{Name: "Hired", Kind: "terminal"},
	{Name: "Rejected", Kind: "terminal"},
}

var ErrOrgIncomplete = errors.New("service: org name and admin email are required")

// NewOrg describes the org the admin CLI bootstraps. Slug and AdminName are
// derived from Name and AdminEmail when empty.
type NewOrg struct {
	Name       string
	Slug       string
	AdminEmail string
	AdminName  string
}

// BootstrapResult identifies what BootstrapOrg created. PasswordSetToken is
// the raw one-time token; only its hash is stored.
type BootstrapResult struct {
	OrgID             uuid.UUID
	AdminUserID       uuid.UUID
	TemplateID        uuid.UUID
	PasswordSetToken  string
	PasswordSetExpiry time.Time
}

// BootstrapOrg creates an org, its default settings and pipeline template, and
// its first admin user with a one-time password-set token. It takes a
// transaction rather than a store because it runs on the RLS-bypassing owner
// connection: the org does not exist yet, so no tenant scope can be set. All
// of it lands in tx, so a partial org is never visible.
func BootstrapOrg(ctx context.Context, tx *store.Tx, in NewOrg) (BootstrapResult, error) {
	name := strings.TrimSpace(in.Name)
	email := strings.TrimSpace(in.AdminEmail)
	if name == "" || email == "" {
		return BootstrapResult{}, ErrOrgIncomplete
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = Slugify(name)
	}
	adminName := strings.TrimSpace(in.AdminName)
	if adminName == "" {
		adminName = email
	}

	org, err := tx.Q.CreateOrg(ctx, db.CreateOrgParams{Name: name, Slug: slug})
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("create org: %w", err)
	}
	res := BootstrapResult{OrgID: org.ID}

	if err := writeSettings(ctx, tx, org.ID, DefaultSettings()); err != nil {
		return BootstrapResult{}, err
	}

	tmpl, err := tx.Q.CreatePipelineTemplate(ctx, db.CreatePipelineTemplateParams{
		OrgID: org.ID, Name: DefaultPipelineTemplateName, IsDefault: true,
	})
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("create pipeline template: %w", err)
	}
	res.TemplateID = tmpl.ID
	for i, s := range DefaultPipelineStages {
		_, err := tx.Q.CreatePipelineTemplateStage(ctx, db.CreatePipelineTemplateStageParams{
			OrgID: org.ID, TemplateID: tmpl.ID, Position: int32(i + 1),
			Name: s.Name, Kind: s.Kind, Unblind: s.Unblind,
		})
		if err != nil {
			return BootstrapResult{}, fmt.Errorf("create template stage %q: %w", s.Name, err)
		}
	}

	user, err := tx.Q.CreateOrgUser(ctx, db.CreateOrgUserParams{
		OrgID: org.ID, Email: email, Name: adminName, Timezone: "UTC",
	})
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("create admin user: %w", err)
	}
	res.AdminUserID = user.ID
	if err := tx.Q.AddOrgUserRole(ctx, db.AddOrgUserRoleParams{OrgUserID: user.ID, OrgID: org.ID, Role: RoleAdmin}); err != nil {
		return BootstrapResult{}, fmt.Errorf("grant admin role: %w", err)
	}

	token, expiry, err := issuePasswordSet(ctx, tx, org.ID, uuid.NullUUID{UUID: user.ID, Valid: true}, uuid.NullUUID{})
	if err != nil {
		return BootstrapResult{}, err
	}
	res.PasswordSetToken, res.PasswordSetExpiry = token, expiry
	return res, nil
}

// issuePasswordSet creates a long-lived reset token so a brand-new account can
// choose its first password. Exactly one of orgUser/clientUser is valid.
func issuePasswordSet(ctx context.Context, tx *store.Tx, orgID uuid.UUID, orgUser, clientUser uuid.NullUUID) (string, time.Time, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := time.Now().Add(PasswordSetTTL)
	_, err = tx.Q.CreatePasswordReset(ctx, db.CreatePasswordResetParams{
		OrgID: orgID, OrgUserID: orgUser, ClientUserID: clientUser,
		TokenHash: hash, ExpiresAt: ts(expiry),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create password-set token: %w", err)
	}
	return token, expiry, nil
}

func writeSettings(ctx context.Context, tx *store.Tx, orgID uuid.UUID, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	weights, err := json.Marshal(s.IntegrityWeights)
	if err != nil {
		return err
	}
	for _, kv := range []struct {
		key   string
		value []byte
	}{
		{SettingPoolScoreThreshold, []byte(fmt.Sprint(s.PoolScoreThreshold))},
		{SettingAssessmentInviteDays, []byte(fmt.Sprint(s.AssessmentInviteDays))},
		{SettingSnapshotRetentionDays, []byte(fmt.Sprint(s.SnapshotRetentionDays))},
		{SettingIntegrityWeights, weights},
	} {
		err := tx.Q.UpsertOrgSetting(ctx, db.UpsertOrgSettingParams{OrgID: orgID, Key: kv.key, Value: kv.value})
		if err != nil {
			return fmt.Errorf("write setting %s: %w", kv.key, err)
		}
	}
	return nil
}

// Slugify reduces a name to a URL-safe org slug. Accented letters decompose to
// their base letter rather than vanishing, so "Ünïcode Ltd" stays readable.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
