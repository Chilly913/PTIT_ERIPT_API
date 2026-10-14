package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
)

type fakeStore struct {
	roles, perms map[int64][]string
	units        map[int64][]int64
	loads        int
	err          error
}

func (f *fakeStore) loadRolesPermissions(_ context.Context, id int64) ([]string, []string, error) {
	f.loads++
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.roles[id], f.perms[id], nil
}

func (f *fakeStore) activeUnitIDs(_ context.Context, id int64) ([]int64, error) {
	return f.units[id], nil
}

const (
	uAdmin int64 = iota + 1
	uInstructor
	uStudent
	uNone
	uMulti
)

func newFake() *fakeStore {
	return &fakeStore{
		roles: map[int64][]string{
			uAdmin:      {RoleAdmin},
			uInstructor: {RoleInstructor},
			uStudent:    {RoleStudent},
			uMulti:      {RoleStudent, RoleInstructor},
		},
		perms: map[int64][]string{
			uAdmin:      {}, // admin bypass must not depend on grants
			uInstructor: DefaultMatrix[RoleInstructor],
			uStudent:    append(slices.Clone(DefaultMatrix[RoleStudent]), PermViewGrades), // duplicate on purpose
			uMulti:      append(slices.Clone(DefaultMatrix[RoleStudent]), DefaultMatrix[RoleInstructor]...),
		},
		units: map[int64][]int64{uInstructor: {3, 7}},
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPrimaryRole(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{RoleStudent}, RoleStudent},
		{[]string{RoleStudent, RoleInstructor}, RoleInstructor},
		{[]string{RoleInstructor, RoleAdvisor}, RoleAdvisor},
		{[]string{RoleAdvisor, RoleFinance}, RoleFinance},
		{[]string{RoleFinance, RoleAcademicManager}, RoleAcademicManager},
		{[]string{RoleStudent, RoleAdmin, RoleAcademicManager}, RoleAdmin},
		{[]string{"custom", RoleStudent}, RoleStudent},
		{[]string{"custom"}, "custom"},
	}
	for _, c := range cases {
		if got := PrimaryRole(c.in); got != c.want {
			t.Errorf("PrimaryRole(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultMatrixMatchesSeeder(t *testing.T) {
	if len(AllRoles) != 6 || len(AllPermissions) != 15 {
		t.Fatalf("roles=%d perms=%d", len(AllRoles), len(AllPermissions))
	}
	total := 0
	for _, r := range AllRoles {
		ps, ok := DefaultMatrix[r]
		if !ok {
			t.Fatalf("matrix misses role %s", r)
		}
		for _, p := range ps {
			if !slices.Contains(AllPermissions, p) {
				t.Errorf("role %s: unknown permission %s", r, p)
			}
		}
		total += len(ps)
	}
	if total != 39 {
		t.Errorf("matrix has %d grants, want 39", total)
	}
}

func TestUserRolesPermissionsCachesAndInvalidates(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	cache := NewMemoryCache()
	s := newService(nil, st, cache, quietLog())

	r, p, err := s.UserRolesPermissions(ctx, uStudent)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r, []string{RoleStudent}) || !slices.Equal(p, []string{PermSubmitReviews, PermTakeExams, PermViewGrades}) {
		t.Fatalf("roles=%v perms=%v", r, p)
	}
	if _, _, err := s.UserRolesPermissions(ctx, uStudent); err != nil || st.loads != 1 {
		t.Fatalf("second lookup should hit cache: loads=%d err=%v", st.loads, err)
	}

	st.roles[uStudent] = []string{RoleStudent, RoleAdvisor}
	_ = s.InvalidateUser(ctx, uStudent)
	r, _, _ = s.UserRolesPermissions(ctx, uStudent)
	if st.loads != 2 || !slices.Equal(r, []string{RoleAdvisor, RoleStudent}) {
		t.Fatalf("after invalidate: loads=%d roles=%v", st.loads, r)
	}

	_, _, _ = s.UserRolesPermissions(ctx, uInstructor) // load 3
	_ = s.InvalidateAll(ctx)
	_, _, _ = s.UserRolesPermissions(ctx, uInstructor) // load 4
	_, _, _ = s.UserRolesPermissions(ctx, uStudent)    // load 5
	if st.loads != 5 {
		t.Fatalf("InvalidateAll should drop every user, loads=%d", st.loads)
	}

	// Never nil, even for a user without roles.
	r, p, _ = s.UserRolesPermissions(ctx, uNone)
	if r == nil || p == nil || len(r)+len(p) != 0 {
		t.Fatalf("empty user: %#v %#v", r, p)
	}
}

func TestStaleStampIsNotServed(t *testing.T) {
	ctx := context.Background()
	c := NewMemoryCache()
	_, stamp, _ := c.Load(ctx, 1)
	_ = c.InvalidateUser(ctx, 1) // happens while the snapshot is being computed
	_ = c.Store(ctx, 1, stamp, Entry{Roles: []string{"old"}})
	if e, _, _ := c.Load(ctx, 1); e != nil {
		t.Fatalf("stale entry served: %+v", e)
	}
}

func TestHelpers(t *testing.T) {
	ctx := context.Background()
	s := newService(nil, newFake(), nil, quietLog())
	check := func(name string, got bool, err error, want bool) {
		t.Helper()
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", name, got, err, want)
		}
	}
	ok, err := s.HasRole(ctx, uAdmin, RoleAdmin)
	check("admin HasRole admin", ok, err, true)
	ok, err = s.HasRole(ctx, uAdmin, RoleStudent)
	check("admin HasRole student (no bypass)", ok, err, false)
	ok, err = s.HasAnyRole(ctx, uMulti, "finance|instructor")
	check("multi HasAnyRole finance|instructor", ok, err, true)
	ok, err = s.Can(ctx, uAdmin, "anything_at_all")
	check("admin Can unknown (Gate::before)", ok, err, true)
	ok, err = s.Can(ctx, uStudent, PermManageCourses)
	check("student Can manage_courses", ok, err, false)
	ok, err = s.CanAny(ctx, uStudent, PermManageCourses, PermTakeExams)
	check("student CanAny", ok, err, true)
	ok, err = s.IsAdmin(ctx, uInstructor)
	check("instructor IsAdmin", ok, err, false)

	st := newFake()
	st.err = errors.New("db down")
	if _, err := newService(nil, st, nil, quietLog()).Can(ctx, uAdmin, "x"); err == nil {
		t.Error("store error must propagate")
	}
}

func TestActiveUnitIDs(t *testing.T) {
	ctx := context.Background()
	s := newService(nil, newFake(), nil, quietLog())

	ids, unr, err := s.ActiveUnitIDs(ctx, uAdmin)
	if err != nil || !unr || ids != nil {
		t.Fatalf("admin: %v %v %v", ids, unr, err)
	}
	ids, unr, _ = s.ActiveUnitIDs(ctx, uInstructor)
	if unr || !slices.Equal(ids, []int64{3, 7}) {
		t.Fatalf("instructor: %v %v", ids, unr)
	}
	sc, _ := s.UnitScopeFor(ctx, uStudent)
	if sc.Unrestricted || sc.UnitIDs == nil || len(sc.UnitIDs) != 0 || sc.Allows(3) {
		t.Fatalf("student scope: %+v", sc)
	}
	if !(UnitScope{Unrestricted: true}).Allows(99) || !(UnitScope{UnitIDs: []int64{3}}).Allows(3) {
		t.Fatal("Allows")
	}
}

// --- middleware --------------------------------------------------------------

func init() { gin.SetMode(gin.TestMode) }

func serve(t *testing.T, s *Service, mw gin.HandlerFunc, userID int64) (int, string) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if userID != 0 {
			auth.SetPrincipal(c, auth.Principal{UserID: userID})
		}
	})
	r.GET("/x", mw, func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	msg, _ := body["message"].(string)
	return w.Code, msg
}

func TestMiddleware(t *testing.T) {
	s := newService(nil, newFake(), NewMemoryCache(), quietLog())
	cases := []struct {
		name    string
		mw      gin.HandlerFunc
		user    int64
		code    int
		message string
	}{
		{"unauthenticated", s.RequireRole(RoleAdmin), 0, 401, "Unauthenticated."},
		{"role admin ok", s.RequireRole(RoleAdmin), uAdmin, 200, ""},
		{"role admin denied", s.RequireRole(RoleAdmin), uInstructor, 403, MsgMissingRoles},
		{"role pipe any-of", s.RequireRole("finance|instructor"), uMulti, 200, ""},
		{"role no admin bypass", s.RequireRole(RoleStudent), uAdmin, 403, MsgMissingRoles},
		{"perm ok", s.RequirePermission(PermManageCourses), uInstructor, 200, ""},
		{"perm any-of", s.RequirePermission("manage_courses|view_dashboard"), uInstructor, 200, ""},
		{"perm denied", s.RequirePermission(PermManageCourses), uStudent, 403, MsgMissingPermissions},
		{"perm admin bypass", s.RequirePermission("not_seeded"), uAdmin, 200, ""},
		{"perm unauthenticated", s.RequirePermission(PermTakeExams), 0, 401, "Unauthenticated."},
		{"role_or_perm via role", s.RequireRoleOrPermission("student|manage_finances"), uStudent, 200, ""},
		{"role_or_perm via perm", s.RequireRoleOrPermission("finance", PermManageExams), uInstructor, 200, ""},
		{"role_or_perm denied", s.RequireRoleOrPermission("finance|manage_finances"), uStudent, 403, MsgMissingAccess},
		{"role_or_perm admin", s.RequireRoleOrPermission("finance"), uAdmin, 200, ""},
	}
	for _, c := range cases {
		code, msg := serve(t, s, c.mw, c.user)
		if code != c.code || (c.message != "" && msg != c.message) {
			t.Errorf("%s: got %d %q, want %d %q", c.name, code, msg, c.code, c.message)
		}
	}

	st := newFake()
	st.err = errors.New("db down")
	if code, _ := serve(t, newService(nil, st, nil, quietLog()), newService(nil, st, nil, quietLog()).RequireRole(RoleAdmin), uAdmin); code != 500 {
		t.Errorf("store error: got %d, want 500", code)
	}
}

func TestCallerMemoizesPerRequest(t *testing.T) {
	st := newFake()
	s := newService(nil, st, nil, quietLog()) // no cache: every lookup hits the store
	r := gin.New()
	r.Use(func(c *gin.Context) { auth.SetPrincipal(c, auth.Principal{UserID: uInstructor}) })
	r.GET("/x", s.RequireRole(RoleInstructor), s.RequirePermission(PermManageExams), func(c *gin.Context) {
		if _, _, ok, err := s.Caller(c); !ok || err != nil {
			t.Error("caller")
		}
		c.Status(http.StatusNoContent)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusNoContent || st.loads != 1 {
		t.Fatalf("code=%d loads=%d", w.Code, st.loads)
	}
}
