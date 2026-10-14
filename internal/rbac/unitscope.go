package rbac

import (
	"context"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UnitScope is the caller's data-visibility scope (port of App\Concerns\ScopesByUnit).
type UnitScope struct {
	// Unrestricted is true for admins: no unit filter applies.
	Unrestricted bool
	// UnitIDs are the units of the caller's active assignments (sorted, unique).
	UnitIDs []int64
}

// ActiveUnitIDs returns the units of the user's active assignments and whether the
// user is unrestricted (admin). Admins return (nil, true) without querying units.
//
// Active = status 'active' AND start_date <= today (or NULL) AND end_date >= today
// (or NULL). Legacy only checked status; the date window is a deliberate tightening.
func (s *Service) ActiveUnitIDs(ctx context.Context, userID int64) (unitIDs []int64, unrestricted bool, err error) {
	roles, _, err := s.UserRolesPermissions(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if slices.Contains(roles, RoleAdmin) {
		return nil, true, nil
	}
	ids, err := s.st.activeUnitIDs(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, false, nil
}

// UnitScopeFor wraps ActiveUnitIDs in a UnitScope.
func (s *Service) UnitScopeFor(ctx context.Context, userID int64) (UnitScope, error) {
	ids, unrestricted, err := s.ActiveUnitIDs(ctx, userID)
	if err != nil {
		return UnitScope{}, err
	}
	return UnitScope{Unrestricted: unrestricted, UnitIDs: ids}, nil
}

// Allows reports whether a row belonging to unitID is visible.
func (u UnitScope) Allows(unitID int64) bool {
	return u.Unrestricted || slices.Contains(u.UnitIDs, unitID)
}

// ScopeByUnit returns a GORM scope limiting rows to column IN (UnitIDs)
// (legacy scopeByUnitColumn): unrestricted -> no filter; no units -> no rows.
// column may be qualified ("courses.unit_id"); it is quoted by GORM.
//
//	db.Scopes(scope.ScopeByUnit("unit_id")).Find(&rows)
func (u UnitScope) ScopeByUnit(column string) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if u.Unrestricted {
			return db
		}
		if len(u.UnitIDs) == 0 {
			return db.Where("1 = 0")
		}
		vals := make([]any, len(u.UnitIDs))
		for i, id := range u.UnitIDs {
			vals[i] = id
		}
		return db.Where(clause.IN{Column: clause.Column{Name: column}, Values: vals})
	}
}
