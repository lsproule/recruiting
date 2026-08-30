package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"recruiting/internal/store"
	"recruiting/internal/store/db"
)

// Roles an org user may hold.
const (
	RoleAdmin     = "admin"
	RoleRecruiter = "recruiter"
	RoleVetter    = "vetter"
)

// Roles is every assignable role, in the order admin screens list them.
var Roles = []string{RoleAdmin, RoleRecruiter, RoleVetter}

var (
	ErrForbidden       = errors.New("service: not permitted")
	ErrInvalidRole     = errors.New("service: unknown role")
	ErrNameRequired    = errors.New("service: name is required")
	ErrEmailRequired   = errors.New("service: email is required")
	ErrCompanyRequired = errors.New("service: a client user must belong to a client company")
	ErrEmailTaken      = errors.New("service: that email is already in use")
	ErrNotFound        = errors.New("service: not found")
)

// OrgService is the admin surface: org users and their roles, client
// companies and their users, and org settings.
type OrgService struct{ st *store.Store }

func NewOrgService(st *store.Store) *OrgService { return &OrgService{st: st} }

// OrgUser is an org user with the roles they hold.
type OrgUser struct {
	ID    uuid.UUID
	Email string
	Name  string
	Roles []string
}

// ClientUser is a client-portal user and the company they belong to.
type ClientUser struct {
	ID              uuid.UUID
	ClientCompanyID uuid.UUID
	CompanyName     string
	Email           string
	Name            string
}

// ClientCompany is a client of the org.
type ClientCompany struct {
	ID   uuid.UUID
	Name string
}

// NewUser describes an org user to create.
type NewUser struct {
	Name  string
	Email string
	Roles []string
}

// NewClientUser describes a client-portal user to create.
type NewClientUser struct {
	ClientCompanyID uuid.UUID
	Name            string
	Email           string
}

// requireAdmin rejects anyone but an org user holding the admin role.
func requireAdmin(p Principal) error {
	if p.Kind != PrincipalOrgUser || !p.HasRole(RoleAdmin) {
		return ErrForbidden
	}
	return nil
}

// ListUsers returns the org's users with their roles.
func (s *OrgService) ListUsers(ctx context.Context, p Principal) ([]OrgUser, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	var out []OrgUser
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListOrgUsers(ctx, p.OrgID)
		if err != nil {
			return err
		}
		roleRows, err := tx.Q.ListOrgUserRolesForOrg(ctx, p.OrgID)
		if err != nil {
			return err
		}
		byUser := make(map[uuid.UUID][]string, len(rows))
		for _, r := range roleRows {
			byUser[r.OrgUserID] = append(byUser[r.OrgUserID], r.Role)
		}
		out = make([]OrgUser, 0, len(rows))
		for _, u := range rows {
			out = append(out, OrgUser{ID: u.ID, Email: u.Email, Name: u.Name, Roles: byUser[u.ID]})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list org users: %w", err)
	}
	return out, nil
}

// User loads one of the org's users. Any org user may read a colleague; the
// chrome needs a display name for whoever is signed in.
func (s *OrgService) User(ctx context.Context, p Principal, userID uuid.UUID) (OrgUser, error) {
	if p.Kind != PrincipalOrgUser {
		return OrgUser{}, ErrForbidden
	}
	var out OrgUser
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: userID, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		roles, err := tx.Q.ListOrgUserRoles(ctx, row.ID)
		if err != nil {
			return err
		}
		out = OrgUser{ID: row.ID, Email: row.Email, Name: row.Name, Roles: roles}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return OrgUser{}, ErrNotFound
	}
	if err != nil {
		return OrgUser{}, fmt.Errorf("get org user: %w", err)
	}
	return out, nil
}

// CreateUser adds an org user with the given roles and returns a one-time
// token the caller turns into a password-set link.
func (s *OrgService) CreateUser(ctx context.Context, p Principal, in NewUser) (OrgUser, string, error) {
	if err := requireAdmin(p); err != nil {
		return OrgUser{}, "", err
	}
	name, email := strings.TrimSpace(in.Name), strings.TrimSpace(in.Email)
	roles, err := normaliseRoles(in.Roles)
	if err != nil {
		return OrgUser{}, "", err
	}
	if err := requireNameEmail(name, email); err != nil {
		return OrgUser{}, "", err
	}
	var user OrgUser
	var token string
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CreateOrgUser(ctx, db.CreateOrgUserParams{OrgID: p.OrgID, Email: email, Name: name, Timezone: "UTC"})
		if err != nil {
			return err
		}
		for _, r := range roles {
			if err := tx.Q.AddOrgUserRole(ctx, db.AddOrgUserRoleParams{OrgUserID: row.ID, OrgID: p.OrgID, Role: r}); err != nil {
				return err
			}
		}
		token, _, err = issuePasswordSet(ctx, tx, p.OrgID, uuid.NullUUID{UUID: row.ID, Valid: true}, uuid.NullUUID{})
		if err != nil {
			return err
		}
		user = OrgUser{ID: row.ID, Email: row.Email, Name: row.Name, Roles: roles}
		return nil
	})
	if err != nil {
		return OrgUser{}, "", wrapCreate("create org user", err)
	}
	return user, token, nil
}

// SetUserRoles replaces a user's roles. An admin may not drop their own admin
// role, which would leave the org without a way back in.
func (s *OrgService) SetUserRoles(ctx context.Context, p Principal, userID uuid.UUID, roles []string) error {
	if err := requireAdmin(p); err != nil {
		return err
	}
	wanted, err := normaliseRoles(roles)
	if err != nil {
		return err
	}
	if userID == p.UserID && !containsRole(wanted, RoleAdmin) {
		return fmt.Errorf("%w: an admin cannot remove their own admin role", ErrForbidden)
	}
	err = s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		if _, err := tx.Q.GetOrgUser(ctx, db.GetOrgUserParams{ID: userID, OrgID: p.OrgID}); err != nil {
			return err
		}
		if err := tx.Q.DeleteOrgUserRoles(ctx, userID); err != nil {
			return err
		}
		for _, r := range wanted {
			if err := tx.Q.AddOrgUserRole(ctx, db.AddOrgUserRoleParams{OrgUserID: userID, OrgID: p.OrgID, Role: r}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set roles: %w", err)
	}
	return nil
}

// ListClientCompanies returns the org's client companies.
func (s *OrgService) ListClientCompanies(ctx context.Context, p Principal) ([]ClientCompany, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	var out []ClientCompany
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListClientCompanies(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]ClientCompany, 0, len(rows))
		for _, c := range rows {
			out = append(out, ClientCompany{ID: c.ID, Name: c.Name})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list client companies: %w", err)
	}
	return out, nil
}

// CreateClientCompany adds a client company.
func (s *OrgService) CreateClientCompany(ctx context.Context, p Principal, name string) (ClientCompany, error) {
	if err := requireAdmin(p); err != nil {
		return ClientCompany{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ClientCompany{}, ErrNameRequired
	}
	var out ClientCompany
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		row, err := tx.Q.CreateClientCompany(ctx, db.CreateClientCompanyParams{OrgID: p.OrgID, Name: name})
		if err != nil {
			return err
		}
		out = ClientCompany{ID: row.ID, Name: row.Name}
		return nil
	})
	if err != nil {
		return ClientCompany{}, fmt.Errorf("create client company: %w", err)
	}
	return out, nil
}

// ListClientUsers returns the org's client-portal users.
func (s *OrgService) ListClientUsers(ctx context.Context, p Principal) ([]ClientUser, error) {
	if err := requireAdmin(p); err != nil {
		return nil, err
	}
	var out []ClientUser
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		companies, err := tx.Q.ListClientCompanies(ctx, p.OrgID)
		if err != nil {
			return err
		}
		names := make(map[uuid.UUID]string, len(companies))
		for _, c := range companies {
			names[c.ID] = c.Name
		}
		rows, err := tx.Q.ListClientUsers(ctx, p.OrgID)
		if err != nil {
			return err
		}
		out = make([]ClientUser, 0, len(rows))
		for _, u := range rows {
			out = append(out, ClientUser{
				ID: u.ID, ClientCompanyID: u.ClientCompanyID, CompanyName: names[u.ClientCompanyID],
				Email: u.Email, Name: u.Name,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list client users: %w", err)
	}
	return out, nil
}

// CreateClientUser adds a client-portal user to an existing client company and
// returns a one-time token the caller turns into a password-set link.
func (s *OrgService) CreateClientUser(ctx context.Context, p Principal, in NewClientUser) (ClientUser, string, error) {
	if err := requireAdmin(p); err != nil {
		return ClientUser{}, "", err
	}
	name, email := strings.TrimSpace(in.Name), strings.TrimSpace(in.Email)
	if in.ClientCompanyID == uuid.Nil {
		return ClientUser{}, "", ErrCompanyRequired
	}
	if err := requireNameEmail(name, email); err != nil {
		return ClientUser{}, "", err
	}
	var out ClientUser
	var token string
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		company, err := tx.Q.GetClientCompany(ctx, db.GetClientCompanyParams{ID: in.ClientCompanyID, OrgID: p.OrgID})
		if err != nil {
			return err
		}
		row, err := tx.Q.CreateClientUser(ctx, db.CreateClientUserParams{
			OrgID: p.OrgID, ClientCompanyID: company.ID, Email: email, Name: name, Timezone: "UTC",
		})
		if err != nil {
			return err
		}
		token, _, err = issuePasswordSet(ctx, tx, p.OrgID, uuid.NullUUID{}, uuid.NullUUID{UUID: row.ID, Valid: true})
		if err != nil {
			return err
		}
		out = ClientUser{ID: row.ID, ClientCompanyID: company.ID, CompanyName: company.Name, Email: row.Email, Name: row.Name}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientUser{}, "", ErrCompanyRequired
	}
	if err != nil {
		return ClientUser{}, "", wrapCreate("create client user", err)
	}
	return out, token, nil
}

// Settings returns the org's settings, filling in defaults for keys that have
// never been written.
func (s *OrgService) Settings(ctx context.Context, p Principal) (Settings, error) {
	if p.Kind != PrincipalOrgUser {
		return Settings{}, ErrForbidden
	}
	out := DefaultSettings()
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		rows, err := tx.Q.ListOrgSettings(ctx, p.OrgID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			switch row.Key {
			case SettingPoolScoreThreshold:
				out.PoolScoreThreshold, err = settingInt(row)
			case SettingAssessmentInviteDays:
				out.AssessmentInviteDays, err = settingInt(row)
			case SettingIntegrityWeights:
				var stored map[string]float64
				if err = json.Unmarshal(row.Value, &stored); err != nil {
					err = fmt.Errorf("%w: %s is not valid JSON", ErrInvalidSettings, row.Key)
					break
				}
				// Only the signals the worker computes; a stored weight for
				// anything else is stale and must not resurface in the form.
				for _, name := range IntegritySignalNames {
					if v, ok := stored[name]; ok {
						out.IntegrityWeights[name] = v
					}
				}
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}
	return out, nil
}

// UpdateSettings validates and stores the org's settings.
func (s *OrgService) UpdateSettings(ctx context.Context, p Principal, in Settings) error {
	if err := requireAdmin(p); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	err := s.st.WithTx(ctx, p, func(ctx context.Context, tx *store.Tx) error {
		return writeSettings(ctx, tx, p.OrgID, in)
	})
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// settingInt reads a scalar integer setting, naming the key when the stored
// value is not one.
func settingInt(row db.OrgSetting) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(string(row.Value)))
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not a whole number", ErrInvalidSettings, row.Key)
	}
	return n, nil
}

func requireNameEmail(name, email string) error {
	if name == "" {
		return ErrNameRequired
	}
	if email == "" || !strings.Contains(email, "@") {
		return ErrEmailRequired
	}
	return nil
}

func normaliseRoles(roles []string) ([]string, error) {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(strings.ToLower(r))
		if r == "" {
			continue
		}
		if !containsRole(Roles, r) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRole, r)
		}
		if !containsRole(out, r) {
			out = append(out, r)
		}
	}
	// Keep the canonical order so screens and tests see a stable list.
	ordered := make([]string, 0, len(out))
	for _, r := range Roles {
		if containsRole(out, r) {
			ordered = append(ordered, r)
		}
	}
	return ordered, nil
}

func containsRole(list []string, role string) bool {
	for _, r := range list {
		if r == role {
			return true
		}
	}
	return false
}

// uniqueEmailIndexes are the per-org, case-insensitive email indexes whose
// violation means the address is already taken rather than a real failure.
var uniqueEmailIndexes = map[string]bool{
	"org_user_org_email_idx":    true,
	"client_user_org_email_idx": true,
}

// wrapCreate maps a unique-email violation onto ErrEmailTaken.
func wrapCreate(what string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && uniqueEmailIndexes[pgErr.ConstraintName] {
		return ErrEmailTaken
	}
	return fmt.Errorf("%s: %w", what, err)
}
