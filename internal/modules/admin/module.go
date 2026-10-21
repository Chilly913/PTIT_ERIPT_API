// Package admin serves the back-office system endpoints: dashboard stats, users, roles and site settings.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
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
	db       *gorm.DB
	tokens   *auth.TokenService
	roles    auth.RoleService
	resetter PasswordResetter
}

// PasswordResetter e-mails a password reset link (implemented by the auth module).
type PasswordResetter interface {
	SendPasswordReset(ctx context.Context, userID int64) error
}

// New builds the module.
func New(a *app.App, tokens *auth.TokenService, roles auth.RoleService, resetter PasswordResetter) *Module {
	return &Module{db: a.DB, tokens: tokens, roles: roles, resetter: resetter}
}

// Register mounts /admin/* (admin role only).
func (m *Module) Register(api *gin.RouterGroup) {
	g := api.Group("/admin", m.tokens.Required(), auth.RequireRole(m.roles, auth.RoleAdmin))
	g.GET("/stats", m.stats)

	g.GET("/users", m.listUsers)
	g.POST("/users", m.createUser)
	g.POST("/users/bulk", m.bulkUsers)
	g.POST("/users/import", m.importStudents)
	g.GET("/users/import-template", m.importTemplate)
	g.GET("/users/:id", m.showUser)
	g.PUT("/users/:id", m.updateUser)
	g.DELETE("/users/:id", m.deleteUser)
	g.POST("/users/:id/disable", m.setDisabled(true))
	g.POST("/users/:id/enable", m.setDisabled(false))
	g.PUT("/users/:id/roles", m.setUserRoles)
	g.POST("/users/:id/reset-password", m.resetPassword)

	g.GET("/roles", m.listRoles)
	g.GET("/permissions", m.listPermissions)
	g.PUT("/roles/:role/permissions", m.setRolePermissions)

	g.GET("/settings", m.getSettings)
	g.PUT("/settings", m.putSettings)
}

func (m *Module) jsonRows(sql string, args ...any) ([]json.RawMessage, error) {
	rows, err := m.db.Raw(sql, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, rows.Err()
}

func (m *Module) jsonOne(sql string, args ...any) (json.RawMessage, error) {
	rows, err := m.jsonRows(sql, args...)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

func randomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b) + "1a"
}

func nullable(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}

func okJSON(c *gin.Context, v json.RawMessage) {
	if v == nil {
		httpx.NotFound(c, "")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", v)
}

// ---- stats ---------------------------------------------------------------------

func (m *Module) stats(c *gin.Context) {
	out, err := m.jsonOne(`SELECT jsonb_build_object(
	  'generated_at', now(),
	  'users', jsonb_build_object(
	    'total', count(*), 'active', count(*) FILTER (WHERE disabled_at IS NULL),
	    'disabled', count(*) FILTER (WHERE disabled_at IS NOT NULL),
	    'unverified', count(*) FILTER (WHERE email_verified_at IS NULL),
	    'new_7d', count(*) FILTER (WHERE created_at > now() - interval '7 days'),
	    'new_30d', count(*) FILTER (WHERE created_at > now() - interval '30 days'),
	    'by_role', (SELECT coalesce(jsonb_agg(jsonb_build_object('role', r.name, 'count', r.n) ORDER BY r.n DESC), '[]'::jsonb)
	      FROM (SELECT ro.name, count(ur.user_id) n FROM roles ro LEFT JOIN user_roles ur ON ur.role_id = ro.id
	            LEFT JOIN users uu ON uu.id = ur.user_id AND uu.deleted_at IS NULL GROUP BY ro.name) r)),
	  'courses', jsonb_build_object('total', (SELECT count(*) FROM courses),
	    'by_status', (SELECT coalesce(jsonb_agg(jsonb_build_object('status', s.status, 'count', s.n)), '[]'::jsonb)
	      FROM (SELECT status, count(*) n FROM courses GROUP BY status) s)),
	  'enrollments', jsonb_build_object('total', (SELECT count(*) FROM enrollments),
	    'new_7d', (SELECT count(*) FROM enrollments WHERE enrolled_at > now() - interval '7 days'),
	    'new_30d', (SELECT count(*) FROM enrollments WHERE enrolled_at > now() - interval '30 days')),
	  'learning', jsonb_build_object('lessons_completed_30d', (SELECT count(*) FROM lesson_progress WHERE completed_at > now() - interval '30 days')),
	  'exams', jsonb_build_object('total', (SELECT count(*) FROM exams), 'active', (SELECT count(*) FROM exams WHERE status = 'published'),
	    'attempts_30d', 0),
	  'series', jsonb_build_object(
	    'signups_30d', (SELECT jsonb_agg(jsonb_build_object('date', to_char(d, 'YYYY-MM-DD'), 'count',
	        (SELECT count(*) FROM users WHERE deleted_at IS NULL AND created_at::date = d)) ORDER BY d)
	      FROM generate_series(current_date - 29, current_date, interval '1 day') d),
	    'enrollments_30d', (SELECT jsonb_agg(jsonb_build_object('date', to_char(d, 'YYYY-MM-DD'), 'count',
	        (SELECT count(*) FROM enrollments WHERE enrolled_at::date = d)) ORDER BY d)
	      FROM generate_series(current_date - 29, current_date, interval '1 day') d)),
	  'recent_users', (SELECT coalesce(jsonb_agg(` + userJSON + ` ORDER BY u.created_at DESC), '[]'::jsonb)
	      FROM (SELECT * FROM users WHERE deleted_at IS NULL ORDER BY created_at DESC LIMIT 6) u),
	  'pending_courses', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', c.id, 'title', c.title, 'instructor_name', i.name, 'created_at', c.created_at) ORDER BY c.created_at), '[]'::jsonb)
	      FROM (SELECT * FROM courses WHERE status = 'pending_review' ORDER BY created_at LIMIT 6) c LEFT JOIN users i ON i.id = c.instructor_id))
	FROM users WHERE deleted_at IS NULL`)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	okJSON(c, out)
}

// ---- roles & permissions ---------------------------------------------------------

var roleLabels = map[string]string{
	"admin": "Quản trị viên", "academic_manager": "Giáo vụ", "instructor": "Giảng viên",
	"advisor": "Cố vấn học tập", "finance": "Tài chính", "student": "Sinh viên",
}

var permissionLabels = map[string][2]string{
	"view_dashboard":     {"Xem bảng điều khiển", "Truy cập trang tổng quan quản trị"},
	"manage_users":       {"Quản lý người dùng", "Tạo, sửa, khoá và xoá tài khoản"},
	"manage_roles":       {"Quản lý vai trò", "Gán vai trò và phân quyền"},
	"manage_courses":     {"Quản lý khoá học", "Tạo, sửa, duyệt khoá học"},
	"manage_lessons":     {"Quản lý bài học", "Soạn nội dung và bài học"},
	"manage_exams":       {"Quản lý bài thi", "Tạo đề và tổ chức thi"},
	"manage_finances":    {"Quản lý tài chính", "Đơn hàng, thanh toán"},
	"manage_academic":    {"Quản lý đào tạo", "Năm học, học kỳ, lớp, khoá"},
	"manage_enrollments": {"Quản lý ghi danh", "Ghi danh sinh viên vào lớp học phần"},
	"manage_grades":      {"Quản lý điểm", "Nhập và chốt điểm"},
	"view_grades":        {"Xem điểm", "Xem bảng điểm"},
	"advise_students":    {"Cố vấn sinh viên", "Theo dõi sinh viên được phân công"},
	"take_exams":         {"Làm bài thi", "Tham gia các bài thi"},
	"view_reports":       {"Xem báo cáo", "Truy cập báo cáo thống kê"},
	"submit_reviews":     {"Đánh giá khoá học", "Gửi đánh giá và nhận xét"},
}

func (m *Module) listRoles(c *gin.Context) {
	type row struct {
		Name       string   `json:"name"`
		Label      string   `json:"label"`
		Perms      []string `json:"permissions"`
		UsersCount int64    `json:"users_count"`
	}
	var names []string
	if err := m.db.Raw("SELECT name FROM roles ORDER BY id").Scan(&names).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]row, 0, len(names))
	for _, n := range names {
		r := row{Name: n, Label: roleLabels[n], Perms: []string{}}
		if r.Label == "" {
			r.Label = n
		}
		if err := m.db.Raw(`SELECT p.name FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
			JOIN roles ro ON ro.id = rp.role_id WHERE ro.name = ? ORDER BY p.id`, n).Scan(&r.Perms).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		if err := m.db.Raw(`SELECT count(*) FROM user_roles ur JOIN roles ro ON ro.id = ur.role_id
			JOIN users u ON u.id = ur.user_id AND u.deleted_at IS NULL WHERE ro.name = ?`, n).Scan(&r.UsersCount).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, out)
}

func (m *Module) listPermissions(c *gin.Context) {
	var names []string
	if err := m.db.Raw("SELECT name FROM permissions ORDER BY id").Scan(&names).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]gin.H, 0, len(names))
	for _, n := range names {
		l := permissionLabels[n]
		if l[0] == "" {
			l[0] = n
		}
		out = append(out, gin.H{"name": n, "label": l[0], "description": l[1]})
	}
	c.JSON(http.StatusOK, out)
}

func (m *Module) setRolePermissions(c *gin.Context) {
	role := c.Param("role")
	var b struct {
		Permissions []string `json:"permissions"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if role == auth.RoleAdmin {
		httpx.Abort(c, http.StatusUnprocessableEntity, "The admin role always keeps every permission.")
		return
	}
	var roleID int64
	if err := m.db.Raw("SELECT id FROM roles WHERE name = ?", role).Scan(&roleID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if roleID == 0 {
		httpx.NotFound(c, "")
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM role_permissions WHERE role_id = ?", roleID).Error; err != nil {
			return err
		}
		if len(b.Permissions) == 0 {
			return nil
		}
		return tx.Exec(`INSERT INTO role_permissions (role_id, permission_id) SELECT ?, id FROM permissions WHERE name IN ? ON CONFLICT DO NOTHING`, roleID, b.Permissions).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var perms []string
	_ = m.db.Raw(`SELECT p.name FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id WHERE rp.role_id = ? ORDER BY p.id`, roleID).Scan(&perms)
	if perms == nil {
		perms = []string{}
	}
	label := roleLabels[role]
	if label == "" {
		label = role
	}
	var users int64
	_ = m.db.Raw("SELECT count(*) FROM user_roles WHERE role_id = ?", roleID).Scan(&users)
	c.JSON(http.StatusOK, gin.H{"name": role, "label": label, "permissions": perms, "users_count": users})
}

// ---- settings --------------------------------------------------------------------

var settingKeys = map[string]string{
	"site_name": "s", "site_logo": "s", "site_favicon": "s", "primary_color": "s", "footer_text": "s",
	"contact_email": "s", "contact_phone": "s", "contact_address": "s", "social_links": "j",
	"smtp_host": "s", "smtp_port": "n", "smtp_username": "s", "smtp_encryption": "s",
	"mail_from_address": "s", "mail_from_name": "s",
}

func (m *Module) settingsPayload() (gin.H, error) {
	type kv struct {
		Key    string
		Value  *string
		HasEnc bool
	}
	var rows []kv
	if err := m.db.Raw("SELECT key, value, value_encrypted IS NOT NULL AS has_enc FROM site_settings").Scan(&rows).Error; err != nil {
		return nil, err
	}
	settings := gin.H{}
	secrets := gin.H{"smtp_password": false}
	for _, r := range rows {
		if r.Key == "smtp_password" {
			secrets["smtp_password"] = r.HasEnc || (r.Value != nil && *r.Value != "")
			continue
		}
		kind, ok := settingKeys[r.Key]
		if !ok || r.Value == nil {
			continue
		}
		switch kind {
		case "n":
			var n int
			fmt.Sscanf(*r.Value, "%d", &n)
			settings[r.Key] = n
		case "j":
			var v any
			if json.Unmarshal([]byte(*r.Value), &v) == nil {
				settings[r.Key] = v
			}
		default:
			settings[r.Key] = *r.Value
		}
	}
	return gin.H{"settings": settings, "secrets": secrets}, nil
}

func (m *Module) getSettings(c *gin.Context) {
	out, err := m.settingsPayload()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (m *Module) putSettings(c *gin.Context) {
	var b struct {
		Settings map[string]any     `json:"settings"`
		Secrets  map[string]*string `json:"secrets"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	verr := httpx.NewValidationError()
	for k := range b.Settings {
		if _, ok := settingKeys[k]; !ok {
			verr.Add("settings."+k, "The selected setting is invalid.")
		}
	}
	if verr.HasErrors() {
		httpx.Fail(c, verr)
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for k, v := range b.Settings {
			if v == nil {
				if err := tx.Exec("DELETE FROM site_settings WHERE key = ?", k).Error; err != nil {
					return err
				}
				continue
			}
			var val string
			switch x := v.(type) {
			case string:
				val = x
			case float64:
				val = fmt.Sprintf("%d", int64(x))
			default:
				j, _ := json.Marshal(x)
				val = string(j)
			}
			if err := tx.Exec(`INSERT INTO site_settings (key, value, value_encrypted) VALUES (?, ?, NULL)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_encrypted = NULL`, k, val).Error; err != nil {
				return err
			}
		}
		if pw, ok := b.Secrets["smtp_password"]; ok {
			if pw == nil {
				return tx.Exec("DELETE FROM site_settings WHERE key = 'smtp_password'").Error
			}
			// Stored as an opaque value; the mailer reads it back through the same table.
			return tx.Exec(`INSERT INTO site_settings (key, value) VALUES ('smtp_password', ?)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_encrypted = NULL`, *pw).Error
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.settingsPayload()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
