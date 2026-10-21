// Package academic serves admin CRUD for the academic structure: training systems
// (hệ đào tạo), programs, academic years, terms (học kỳ), cohorts (khóa) and
// administrative classes (lớp), each with list/sort/filter, create, update, delete,
// CSV export and CSV import under /api/admin/academic/<resource>.
package academic

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Module implements app.Module.
type Module struct {
	db     *gorm.DB
	tokens *auth.TokenService
	roles  auth.RoleService
}

// New builds the module; tokens and roles guard the routes.
func New(a *app.App, tokens *auth.TokenService, roles auth.RoleService) *Module {
	return &Module{db: a.DB, tokens: tokens, roles: roles}
}

// Register mounts the routes (admin and academic_manager only).
func (m *Module) Register(api *gin.RouterGroup) {
	g := api.Group("/admin/academic", m.tokens.Required(), auth.RequireRole(m.roles, auth.RoleAdmin, auth.RoleAcademicManager))
	for _, r := range resources {
		r.Mount(g, m.db)
	}
	g.GET("/options/:name", m.options)
}

// defaultInstitution returns the id of the single institution used by the app, creating it on first use.
func defaultInstitution(tx *gorm.DB) (int64, error) {
	var id int64
	if err := tx.Raw("SELECT id FROM institutions ORDER BY id LIMIT 1").Scan(&id).Error; err != nil {
		return 0, err
	}
	if id != 0 {
		return id, nil
	}
	err := tx.Raw(`INSERT INTO institutions (code, name, institution_type) VALUES ('PTIT', 'Học viện Công nghệ Bưu chính Viễn thông', 'academy')
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&id).Error
	return id, err
}

type option struct {
	Value int64  `json:"value"`
	Label string `json:"label"`
}

// options lists {value,label} pairs for select inputs of the admin forms.
func (m *Module) options(c *gin.Context) {
	var sql string
	var args []any
	switch c.Param("name") {
	case "program-types":
		sql = "SELECT id AS value, name AS label FROM program_types ORDER BY name"
	case "programs":
		sql = "SELECT id AS value, (code || ' – ' || name) AS label FROM programs ORDER BY code"
	case "academic-years":
		sql = "SELECT id AS value, name AS label FROM academic_years ORDER BY start_date DESC"
	case "cohorts":
		sql = "SELECT id AS value, (code || ' – ' || name) AS label FROM cohorts"
		if pid := strings.TrimSpace(c.Query("program_id")); pid != "" {
			sql += " WHERE program_id = ?"
			args = append(args, pid)
		}
		sql += " ORDER BY start_year DESC, code"
	case "advisors":
		// Academic advisors are users holding the advisor or instructor role.
		sql = `SELECT u.id AS value, (u.name || ' – ' || u.email) AS label FROM users u
			WHERE u.deleted_at IS NULL AND EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id AND r.name IN ('advisor', 'instructor'))
			ORDER BY u.name`
	default:
		httpx.NotFound(c, "")
		return
	}
	out := []option{}
	if err := m.db.Raw(sql, args...).Scan(&out).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
