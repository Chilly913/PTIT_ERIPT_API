package rbac

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/migrations"
)

// Integration tests run against TEST_DATABASE_URL (migrated to the latest version
// first) and, when set, TEST_REDIS_URL (e.g. redis://127.0.0.1:6380/6); otherwise
// the in-memory cache is used. All rows/keys created here are removed at the end.
type itEnv struct {
	db     *gorm.DB
	svc    *Service
	rdb    *redis.Client
	prefix string
	run    string
	users  map[string]int64
}

func setupIntegration(t *testing.T) *itEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	log := quietLog()

	m, err := db.NewMigrator(dsn, migrations.FS, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	_ = m.Close()

	gdb, err := db.Open(ctx, config.DBConfig{URL: dsn, MaxOpenConns: 5, MaxIdleConns: 2, LogLevel: "silent"}, log)
	if err != nil {
		t.Fatal(err)
	}
	e := &itEnv{db: gdb, run: fmt.Sprint(time.Now().UnixNano()), users: map[string]int64{}}
	e.prefix = "rbactest:" + e.run + ":"

	var cache Cache = NewMemoryCache()
	if ru := os.Getenv("TEST_REDIS_URL"); ru != "" {
		opts, err := redis.ParseURL(ru)
		if err != nil {
			t.Fatal(err)
		}
		e.rdb = redis.NewClient(opts)
		if err := e.rdb.Ping(ctx).Err(); err != nil {
			t.Fatal(err)
		}
		cache = NewRedisCache(e.rdb, e.prefix, time.Minute)
	} else {
		t.Log("TEST_REDIS_URL not set: using MemoryCache")
	}
	e.svc = New(gdb, cache, log)

	t.Cleanup(func() {
		ids := slices.Collect(maps.Values(e.users))
		if len(ids) > 0 {
			gdb.Exec(`DELETE FROM users WHERE id IN ?`, ids)
		}
		gdb.Exec(`DELETE FROM units WHERE code LIKE ?`, "T"+e.run+"%")
		gdb.Exec(`DELETE FROM institutions WHERE code = ?`, "T"+e.run)
		gdb.Exec(`DELETE FROM positions WHERE code = ?`, "T"+e.run)
		gdb.Exec(`DELETE FROM roles WHERE name = ?`, "test_role_"+e.run)
		if e.rdb != nil {
			keys, _ := e.rdb.Keys(ctx, e.prefix+"*").Result()
			if len(keys) > 0 {
				e.rdb.Del(ctx, keys...)
			}
			_ = e.rdb.Close()
		}
		_ = db.Close(gdb)
	})
	return e
}

func (e *itEnv) mkUser(t *testing.T, key string, roles ...string) int64 {
	t.Helper()
	var id int64
	err := e.db.Raw(`INSERT INTO users (email, name) VALUES (?, ?) RETURNING id`,
		fmt.Sprintf("%s.%s@rbac.test", key, e.run), key).Scan(&id).Error
	if err != nil {
		t.Fatal(err)
	}
	e.users[key] = id
	if err := e.svc.SyncRoles(context.Background(), nil, id, roles...); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntegrationMatrix(t *testing.T) {
	e := setupIntegration(t)
	type row struct{ Role, Perm string }
	var rows []row
	if err := e.db.Raw(`SELECT r.name AS role, p.name AS perm FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id JOIN permissions p ON p.id = rp.permission_id`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rows {
		got[r.Role] = append(got[r.Role], r.Perm)
	}
	for _, role := range AllRoles {
		if !slices.Equal(normalize(got[role]), normalize(DefaultMatrix[role])) {
			t.Errorf("role %s: db %v, want %v", role, normalize(got[role]), normalize(DefaultMatrix[role]))
		}
	}

	ctx := context.Background()
	inst := e.mkUser(t, "inst", RoleInstructor)
	_, p, err := e.svc.UserRolesPermissions(ctx, inst)
	if err != nil || !slices.Equal(p, normalize(DefaultMatrix[RoleInstructor])) {
		t.Fatalf("instructor perms %v %v", p, err)
	}
}

func TestIntegrationWritesAndCache(t *testing.T) {
	e := setupIntegration(t)
	ctx := context.Background()
	u := e.mkUser(t, "multi", RoleStudent)

	want := func(step string, roles, perms []string) {
		t.Helper()
		r, p, err := e.svc.UserRolesPermissions(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r, normalize(roles)) || !slices.Equal(p, normalize(perms)) {
			t.Fatalf("%s: roles=%v perms=%v, want %v %v", step, r, p, normalize(roles), normalize(perms))
		}
	}
	union := func(rs ...string) []string {
		var out []string
		for _, r := range rs {
			out = append(out, DefaultMatrix[r]...)
		}
		return slices.Compact(normalize(out))
	}

	want("initial", []string{RoleStudent}, union(RoleStudent))

	if err := e.svc.AssignRole(ctx, nil, u, RoleAdvisor); err != nil {
		t.Fatal(err)
	}
	want("assign advisor", []string{RoleStudent, RoleAdvisor}, union(RoleStudent, RoleAdvisor))
	if err := e.svc.AssignRole(ctx, nil, u, RoleAdvisor); err != nil { // idempotent
		t.Fatal(err)
	}

	if err := e.svc.AssignRole(ctx, nil, u, "nope"); !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("unknown role: %v", err)
	}
	if err := e.svc.SyncRoles(ctx, nil, u, RoleFinance, "nope"); !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("sync unknown role: %v", err)
	}
	want("failed sync is atomic", []string{RoleStudent, RoleAdvisor}, union(RoleStudent, RoleAdvisor))

	if err := e.svc.GivePermission(ctx, nil, u, PermManageExams); err != nil {
		t.Fatal(err)
	}
	want("direct perm", []string{RoleStudent, RoleAdvisor}, append(union(RoleStudent, RoleAdvisor), PermManageExams))
	if err := e.svc.GivePermission(ctx, nil, u, "nope"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("unknown perm: %v", err)
	}
	if err := e.svc.RevokePermission(ctx, nil, u, PermManageExams); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RemoveRole(ctx, nil, u, RoleStudent); err != nil {
		t.Fatal(err)
	}
	want("revoke+remove", []string{RoleAdvisor}, union(RoleAdvisor))

	if err := e.svc.SyncRoles(ctx, nil, u, "finance|academic_manager"); err != nil {
		t.Fatal(err)
	}
	want("sync", []string{RoleFinance, RoleAcademicManager}, union(RoleFinance, RoleAcademicManager))
	if err := e.svc.SyncRoles(ctx, nil, u); err != nil {
		t.Fatal(err)
	}
	want("sync empty", []string{}, []string{})

	// Caller transaction: rolled back -> no change visible.
	_ = e.db.Transaction(func(tx *gorm.DB) error {
		if err := e.svc.AssignRole(ctx, tx, u, RoleAdmin); err != nil {
			t.Fatal(err)
		}
		return errors.New("rollback")
	})
	want("tx rollback", []string{}, []string{})

	// The cache really serves: a change behind the Service's back is invisible
	// until invalidated.
	if err := e.db.Exec(`INSERT INTO user_roles (user_id, role_id) SELECT ?, id FROM roles WHERE name = 'student'`, u).Error; err != nil {
		t.Fatal(err)
	}
	want("cached", []string{}, []string{})
	if err := e.svc.InvalidateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	want("after invalidate", []string{RoleStudent}, union(RoleStudent))

	// Role -> permission change invalidates every user.
	role := "test_role_" + e.run
	if err := e.db.Exec(`INSERT INTO roles (name) VALUES (?)`, role).Error; err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AssignRole(ctx, nil, u, role); err != nil {
		t.Fatal(err)
	}
	other := e.mkUser(t, "other", role)
	if ok, _ := e.svc.Can(ctx, other, PermManageFinances); ok {
		t.Fatal("other should not have manage_finances yet")
	}
	if err := e.svc.SyncRolePermissions(ctx, nil, role, PermManageFinances, PermViewReports); err != nil {
		t.Fatal(err)
	}
	want("role perms synced", []string{RoleStudent, role}, slices.Compact(normalize(append(union(RoleStudent), PermManageFinances, PermViewReports))))
	if ok, _ := e.svc.Can(ctx, other, PermManageFinances); !ok {
		t.Fatal("other: matrix change not visible")
	}
	if err := e.svc.SyncRolePermissions(ctx, nil, role); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.svc.Can(ctx, other, PermManageFinances); ok {
		t.Fatal("other: emptied role still grants")
	}
	if err := e.svc.SyncRolePermissions(ctx, nil, role, "nope"); !errors.Is(err, ErrPermissionNotFound) {
		t.Fatalf("unknown perm: %v", err)
	}
}

func TestIntegrationMiddleware(t *testing.T) {
	e := setupIntegration(t)
	admin := e.mkUser(t, "admin", RoleAdmin)
	inst := e.mkUser(t, "inst", RoleInstructor)
	stud := e.mkUser(t, "stud", RoleStudent)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		var id int64
		fmt.Sscan(c.GetHeader("X-User"), &id)
		if id != 0 {
			auth.SetPrincipal(c, auth.Principal{UserID: id})
		}
	})
	ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	r.GET("/admin", e.svc.RequireRole("admin"), ok)
	r.GET("/courses", e.svc.RequirePermission("manage_lessons|manage_courses"), ok)
	r.GET("/exams", e.svc.RequirePermission(PermTakeExams), ok)
	r.GET("/mixed", e.svc.RequireRoleOrPermission("student|manage_finances"), ok)

	cases := []struct {
		path string
		user int64
		code int
		body string
	}{
		{"/admin", 0, 401, `{"message":"Unauthenticated."}`},
		{"/admin", admin, 204, ""},
		{"/admin", inst, 403, `{"message":"User does not have the right roles."}`},
		{"/courses", inst, 204, ""},
		{"/courses", stud, 403, `{"message":"User does not have the right permissions."}`},
		{"/exams", stud, 204, ""},
		{"/exams", inst, 403, `{"message":"User does not have the right permissions."}`},
		{"/exams", admin, 204, ""},
		{"/mixed", stud, 204, ""},
		{"/mixed", inst, 403, `{"message":"User does not have any of the necessary access rights."}`},
		{"/mixed", admin, 204, ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.Header.Set("X-User", fmt.Sprint(c.user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.code || (c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s as %d: %d %s, want %d %s", c.path, c.user, w.Code, w.Body.String(), c.code, c.body)
		}
	}
}

func TestIntegrationUnitScope(t *testing.T) {
	e := setupIntegration(t)
	ctx := context.Background()
	admin := e.mkUser(t, "admin", RoleAdmin)
	mgr := e.mkUser(t, "mgr", RoleAcademicManager)
	none := e.mkUser(t, "none", RoleAcademicManager)

	code := "T" + e.run
	var instID, posID int64
	e.db.Raw(`INSERT INTO institutions (code, name) VALUES (?, 'Test') RETURNING id`, code).Scan(&instID)
	e.db.Raw(`INSERT INTO positions (code, name) VALUES (?, 'Test') RETURNING id`, code).Scan(&posID)
	unit := func(suffix string) int64 {
		var id int64
		if err := e.db.Raw(`INSERT INTO units (institution_id, code, name, unit_type) VALUES (?, ?, 'U', 'faculty') RETURNING id`,
			instID, code+suffix).Scan(&id).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	uActive, uOpenEnded, uInactive, uFuture, uExpired := unit("a"), unit("b"), unit("c"), unit("d"), unit("e")
	assign := func(unitID int64, status, start, end string) {
		var s, en any
		if start != "" {
			s = start
		}
		if end != "" {
			en = end
		}
		if err := e.db.Exec(`INSERT INTO user_assignments (user_id, unit_id, position_id, status, start_date, end_date)
			VALUES (?, ?, ?, ?, ?::date, ?::date)`, mgr, unitID, posID, status, s, en).Error; err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC()
	d := func(days int) string { return today.AddDate(0, 0, days).Format(time.DateOnly) }
	assign(uActive, "active", d(-10), d(10))
	assign(uOpenEnded, "active", "", "")
	assign(uInactive, "inactive", "", "")
	assign(uFuture, "active", d(5), "")
	assign(uExpired, "active", d(-30), d(-1))

	ids, unr, err := e.svc.ActiveUnitIDs(ctx, mgr)
	want := normalizeIDs(uActive, uOpenEnded)
	if err != nil || unr || !slices.Equal(ids, want) {
		t.Fatalf("mgr units = %v unrestricted=%v err=%v, want %v", ids, unr, err, want)
	}

	scopeIDs := func(userID int64) []int64 {
		sc, err := e.svc.UnitScopeFor(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		if err := e.db.Table("units").Where("code LIKE ?", code+"%").
			Scopes(sc.ScopeByUnit("units.id")).Order("id").Pluck("id", &got).Error; err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := scopeIDs(mgr); !slices.Equal(got, want) {
		t.Errorf("scoped mgr = %v, want %v", got, want)
	}
	if got := scopeIDs(admin); len(got) != 5 {
		t.Errorf("admin should see all 5 units, got %v", got)
	}
	if got := scopeIDs(none); len(got) != 0 {
		t.Errorf("no assignments should see nothing, got %v", got)
	}
}

func normalizeIDs(ids ...int64) []int64 {
	slices.Sort(ids)
	return ids
}
