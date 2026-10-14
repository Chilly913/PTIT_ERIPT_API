package rbac

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
)

// store is the read side the Service caches; gormStore is the real one, tests use fakes.
type store interface {
	loadRolesPermissions(ctx context.Context, userID int64) (roles, perms []string, err error)
	activeUnitIDs(ctx context.Context, userID int64) ([]int64, error)
}

// Service answers "what may user X do" with a Redis-cached lookup and performs
// role/permission assignments (invalidating the cache).
//
// Write helpers accept an optional tx. With tx == nil they run (atomically) on the
// Service's own handle and invalidate after commit. With a caller tx they invalidate
// immediately; the caller should call InvalidateUser/InvalidateAll again after its
// commit, otherwise a concurrent reader may cache pre-commit data until the TTL.
type Service struct {
	db    *gorm.DB
	st    store
	cache Cache
	log   *slog.Logger
}

// New builds a Service. cache may be nil (no caching); log may be nil (slog.Default).
func New(db *gorm.DB, cache Cache, log *slog.Logger) *Service {
	return newService(db, gormStore{db: db}, cache, log)
}

// FromApp builds the Service from the shared container: Redis cache under
// REDIS_PREFIX with DefaultCacheTTL (no cache when Redis is not configured).
func FromApp(a *app.App) *Service {
	var c Cache
	if a.Redis != nil {
		c = NewRedisCache(a.Redis, a.Cfg.Redis.Prefix, DefaultCacheTTL)
	}
	return New(a.DB, c, a.Log)
}

func newService(db *gorm.DB, st store, cache Cache, log *slog.Logger) *Service {
	if cache == nil {
		cache = noCache{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, st: st, cache: cache, log: log}
}

// UserRolesPermissions returns the user's role names and effective permissions
// (direct user_permissions ∪ permissions of the user's roles), both sorted and
// deduplicated and never nil. Cache failures fall back to the database.
func (s *Service) UserRolesPermissions(ctx context.Context, userID int64) (roles, permissions []string, err error) {
	e, stamp, cerr := s.cache.Load(ctx, userID)
	if cerr != nil {
		s.log.WarnContext(ctx, "rbac cache load failed", "user_id", userID, "error", cerr.Error())
	} else if e != nil {
		return normalize(e.Roles), normalize(e.Permissions), nil
	}
	roles, permissions, err = s.st.loadRolesPermissions(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	roles, permissions = normalize(roles), normalize(permissions)
	if cerr == nil {
		if err := s.cache.Store(ctx, userID, stamp, Entry{Roles: roles, Permissions: permissions}); err != nil {
			s.log.WarnContext(ctx, "rbac cache store failed", "user_id", userID, "error", err.Error())
		}
	}
	return roles, permissions, nil
}

// Roles returns the user's role names (sorted).
func (s *Service) Roles(ctx context.Context, userID int64) ([]string, error) {
	r, _, err := s.UserRolesPermissions(ctx, userID)
	return r, err
}

// HasRole mirrors Spatie hasRole (no admin bypass).
func (s *Service) HasRole(ctx context.Context, userID int64, role string) (bool, error) {
	return s.HasAnyRole(ctx, userID, role)
}

// HasAnyRole mirrors Spatie hasAnyRole (no admin bypass). Accepts "a|b" arguments.
func (s *Service) HasAnyRole(ctx context.Context, userID int64, roles ...string) (bool, error) {
	r, _, err := s.UserRolesPermissions(ctx, userID)
	if err != nil {
		return false, err
	}
	return HasAnyOf(r, splitNames(roles)...), nil
}

// Can mirrors $user->can($perm): true for admin (Gate::before) or when the
// permission is granted directly or via a role. Unknown permissions are false.
func (s *Service) Can(ctx context.Context, userID int64, perm string) (bool, error) {
	return s.CanAny(ctx, userID, perm)
}

// CanAny mirrors $user->canAny([...]) including the admin bypass. Accepts "a|b".
func (s *Service) CanAny(ctx context.Context, userID int64, perms ...string) (bool, error) {
	r, p, err := s.UserRolesPermissions(ctx, userID)
	if err != nil {
		return false, err
	}
	return CanAny(r, p, splitNames(perms)...), nil
}

// IsAdmin is HasRole(admin) (legacy Authorize::isAdmin).
func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	return s.HasRole(ctx, userID, RoleAdmin)
}

// InvalidateUser drops the cached snapshot of one user.
func (s *Service) InvalidateUser(ctx context.Context, userID int64) error {
	return s.cache.InvalidateUser(ctx, userID)
}

// InvalidateAll drops every cached snapshot.
func (s *Service) InvalidateAll(ctx context.Context) error { return s.cache.InvalidateAll(ctx) }

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

// AssignRole adds role to the user (no-op when already held). ErrRoleNotFound
// when the role does not exist (Spatie RoleDoesNotExist).
func (s *Service) AssignRole(ctx context.Context, tx *gorm.DB, userID int64, role string) error {
	return s.writeUser(ctx, tx, userID, func(db *gorm.DB) error {
		id, err := lookupID(db, "roles", role, ErrRoleNotFound)
		if err != nil {
			return err
		}
		return db.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, id).Error
	})
}

// SyncRoles replaces the user's roles with exactly roles (Spatie syncRoles).
// An empty list removes every role. Fails without changes if any role is unknown.
func (s *Service) SyncRoles(ctx context.Context, tx *gorm.DB, userID int64, roles ...string) error {
	names := normalize(splitNames(roles))
	return s.writeUser(ctx, tx, userID, func(db *gorm.DB) error {
		ids, err := lookupIDs(db, "roles", names, ErrRoleNotFound)
		if err != nil {
			return err
		}
		if err := deleteExcept(db, "user_roles", "user_id", userID, "role_id", ids); err != nil {
			return err
		}
		for _, id := range ids {
			if err := db.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveRole removes role from the user (no-op when not held).
func (s *Service) RemoveRole(ctx context.Context, tx *gorm.DB, userID int64, role string) error {
	return s.writeUser(ctx, tx, userID, func(db *gorm.DB) error {
		return db.Exec(`DELETE FROM user_roles WHERE user_id = ? AND role_id IN (SELECT id FROM roles WHERE name = ?)`, userID, role).Error
	})
}

// GivePermission grants a direct permission (Spatie givePermissionTo).
func (s *Service) GivePermission(ctx context.Context, tx *gorm.DB, userID int64, perm string) error {
	return s.writeUser(ctx, tx, userID, func(db *gorm.DB) error {
		id, err := lookupID(db, "permissions", perm, ErrPermissionNotFound)
		if err != nil {
			return err
		}
		return db.Exec(`INSERT INTO user_permissions (user_id, permission_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, id).Error
	})
}

// RevokePermission removes a direct permission (role grants are unaffected).
func (s *Service) RevokePermission(ctx context.Context, tx *gorm.DB, userID int64, perm string) error {
	return s.writeUser(ctx, tx, userID, func(db *gorm.DB) error {
		return db.Exec(`DELETE FROM user_permissions WHERE user_id = ? AND permission_id IN (SELECT id FROM permissions WHERE name = ?)`, userID, perm).Error
	})
}

// SyncRolePermissions replaces a role's permissions (admin "roles" screen,
// Spatie $role->syncPermissions) and invalidates every cached user.
// The legacy refusal to edit the admin role is a controller rule, not enforced here.
func (s *Service) SyncRolePermissions(ctx context.Context, tx *gorm.DB, role string, perms ...string) error {
	names := normalize(splitNames(perms))
	err := s.inTx(ctx, tx, func(db *gorm.DB) error {
		roleID, err := lookupID(db, "roles", role, ErrRoleNotFound)
		if err != nil {
			return err
		}
		ids, err := lookupIDs(db, "permissions", names, ErrPermissionNotFound)
		if err != nil {
			return err
		}
		if err := deleteExcept(db, "role_permissions", "role_id", roleID, "permission_id", ids); err != nil {
			return err
		}
		for _, id := range ids {
			if err := db.Exec(`INSERT INTO role_permissions (role_id, permission_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, roleID, id).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidate(ctx, s.cache.InvalidateAll(ctx), 0)
	return nil
}

func (s *Service) writeUser(ctx context.Context, tx *gorm.DB, userID int64, fn func(db *gorm.DB) error) error {
	if err := s.inTx(ctx, tx, fn); err != nil {
		return err
	}
	s.invalidate(ctx, s.cache.InvalidateUser(ctx, userID), userID)
	return nil
}

// invalidate logs (does not fail the write) when the cache is unreachable: the
// DB change is committed and the TTL bounds the staleness.
func (s *Service) invalidate(ctx context.Context, err error, userID int64) {
	if err != nil {
		s.log.ErrorContext(ctx, "rbac cache invalidation failed", "user_id", userID, "error", err.Error())
	}
}

func (s *Service) inTx(ctx context.Context, tx *gorm.DB, fn func(db *gorm.DB) error) error {
	if tx != nil {
		return fn(tx.WithContext(ctx))
	}
	if s.db == nil {
		return errors.New("rbac: service has no database handle")
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

func lookupID(db *gorm.DB, table, name string, notFound error) (int64, error) {
	ids, err := lookupIDs(db, table, []string{name}, notFound)
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// lookupIDs resolves names (deduplicated) to ids; notFound wraps the first unknown name.
func lookupIDs(db *gorm.DB, table string, names []string, notFound error) ([]int64, error) {
	if len(names) == 0 {
		return []int64{}, nil
	}
	var rows []struct {
		ID   int64
		Name string
	}
	if err := db.Raw(`SELECT id, name FROM `+table+` WHERE name IN ?`, names).Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, n := range names {
		i := slices.IndexFunc(rows, func(r struct {
			ID   int64
			Name string
		}) bool {
			return r.Name == n
		})
		if i < 0 {
			return nil, fmt.Errorf("%w: %q", notFound, n)
		}
		ids = append(ids, rows[i].ID)
	}
	return ids, nil
}

// ---------------------------------------------------------------------------
// GORM store
// ---------------------------------------------------------------------------

type gormStore struct{ db *gorm.DB }

func (g gormStore) loadRolesPermissions(ctx context.Context, userID int64) (roles, perms []string, err error) {
	db := g.db.WithContext(ctx)
	if err = db.Raw(`
		SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? ORDER BY r.name`, userID).Scan(&roles).Error; err != nil {
		return nil, nil, fmt.Errorf("rbac: load roles: %w", err)
	}
	if err = db.Raw(`
		SELECT p.name FROM permissions p
		WHERE p.id IN (
			SELECT up.permission_id FROM user_permissions up WHERE up.user_id = ?
			UNION
			SELECT rp.permission_id FROM role_permissions rp
			JOIN user_roles ur ON ur.role_id = rp.role_id
			WHERE ur.user_id = ?
		)
		ORDER BY p.name`, userID, userID).Scan(&perms).Error; err != nil {
		return nil, nil, fmt.Errorf("rbac: load permissions: %w", err)
	}
	return roles, perms, nil
}

func (g gormStore) activeUnitIDs(ctx context.Context, userID int64) ([]int64, error) {
	var ids []int64
	err := g.db.WithContext(ctx).Raw(`
		SELECT DISTINCT unit_id FROM user_assignments
		WHERE user_id = ? AND status = 'active'
		  AND (start_date IS NULL OR start_date <= CURRENT_DATE)
		  AND (end_date IS NULL OR end_date >= CURRENT_DATE)
		ORDER BY unit_id`, userID).Scan(&ids).Error
	if err != nil {
		return nil, fmt.Errorf("rbac: active unit ids: %w", err)
	}
	return ids, nil
}

// deleteExcept deletes table rows of owner except those whose keepCol is in keep
// (GORM renders an empty IN list as (NULL), so the empty case is explicit).
func deleteExcept(db *gorm.DB, table, ownerCol string, owner int64, keepCol string, keep []int64) error {
	if len(keep) == 0 {
		return db.Exec(`DELETE FROM `+table+` WHERE `+ownerCol+` = ?`, owner).Error
	}
	return db.Exec(`DELETE FROM `+table+` WHERE `+ownerCol+` = ? AND `+keepCol+` NOT IN ?`, owner, keep).Error
}

// noCache disables caching.
type noCache struct{}

func (noCache) Load(context.Context, int64) (*Entry, string, error) { return nil, "", nil }
func (noCache) Store(context.Context, int64, string, Entry) error   { return nil }
func (noCache) InvalidateUser(context.Context, int64) error         { return nil }
func (noCache) InvalidateAll(context.Context) error                 { return nil }
