package service

import (
	"github.com/google/uuid"

	"recruiting/internal/store"
)

type PrincipalKind int

const (
	PrincipalOrgUser PrincipalKind = iota
	PrincipalClientUser
	PrincipalMagicLink
	PrincipalSystem
)

// Principal is the authenticated caller every service method receives.
type Principal struct {
	Kind            PrincipalKind
	OrgID           uuid.UUID
	UserID          uuid.UUID // org_user or client_user id; zero for magic/system
	ClientCompanyID uuid.UUID // set only for PrincipalClientUser
	Roles           []string  // "admin","recruiter","vetter" for org users
	MagicPurpose    string    // "apply","book","assessment" for PrincipalMagicLink
	SubjectID       uuid.UUID // job/application/attempt referenced by a magic link
}

// Scope implements store.Principal.
func (p Principal) Scope() store.Scope {
	s := store.Scope{OrgID: p.OrgID}
	if p.Kind == PrincipalClientUser {
		s.IsClient = true
		s.ClientCompanyID = p.ClientCompanyID
	}
	return s
}

func (p Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}
