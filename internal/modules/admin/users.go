package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// userJSON renders a users row aliased "u" in the AdminUser shape.
const userJSON = `jsonb_build_object('id', u.id, 'name', u.name, 'email', u.email, 'avatar', u.avatar_path, 'user_type', u.user_type,
  'roles', coalesce((SELECT jsonb_agg(r.name ORDER BY r.id) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id), '[]'::jsonb),
  'primary_role', (SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id
      ORDER BY array_position(ARRAY['admin','academic_manager','finance','advisor','instructor','student'], r.name) NULLS LAST LIMIT 1),
  'student_code', u.student_code, 'staff_code', u.staff_code, 'phone', u.phone,
  'email_verified', u.email_verified_at IS NOT NULL, 'disabled', u.disabled_at IS NOT NULL,
  'created_at', u.created_at, 'last_login_at', (SELECT s.last_login_date FROM user_stats s WHERE s.user_id = u.id))`

const detailJSON = `(` + userJSON + ` || jsonb_build_object('profile', jsonb_build_object(
  'gender', u.gender, 'date_of_birth', u.date_of_birth, 'hometown', u.hometown, 'permanent_address', u.permanent_address,
  'nationality', u.nationality, 'bio', u.bio, 'id_card_number', u.id_card_number,
  'institution_id', u.institution_id, 'unit_id', u.unit_id, 'program_id', u.program_id, 'major_id', u.major_id,
  'specialization_id', u.specialization_id, 'cohort_id', u.cohort_id, 'administrative_class_id', u.administrative_class_id,
  'advisor_id', u.advisor_id, 'study_status', u.study_status, 'email_verified_at', u.email_verified_at, 'updated_at', u.updated_at)))`

var userSorts = map[string]string{
	"name": "u.name", "email": "u.email", "created_at": "u.created_at", "student_code": "u.student_code",
}

var userTypes = map[string]bool{"student": true, "instructor": true, "staff": true, "academic_manager": true, "admin": true}

func (m *Module) listUsers(c *gin.Context) {
	p := httpx.ParsePage(c, 15, 100)
	conds, args := []string{"u.deleted_at IS NULL"}, []any{}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		conds = append(conds, "u.search_text ILIKE ?")
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)+"%")
	}
	if r := c.Query("role"); r != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id AND r.name = ?)")
		args = append(args, r)
	}
	switch c.Query("status") {
	case "active":
		conds = append(conds, "u.disabled_at IS NULL")
	case "disabled":
		conds = append(conds, "u.disabled_at IS NOT NULL")
	case "unverified":
		conds = append(conds, "u.email_verified_at IS NULL")
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	order := "u.created_at DESC"
	if s := c.Query("sort"); s != "" {
		dir := " ASC"
		if strings.HasPrefix(s, "-") {
			dir, s = " DESC", s[1:]
		}
		if col, ok := userSorts[s]; ok {
			order = col + dir
		}
	}
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM users u"+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+userJSON+" FROM users u"+where+" ORDER BY "+order+", u.id DESC LIMIT ? OFFSET ?", append(args, p.PerPage, (p.Page-1)*p.PerPage)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	last := int((total + int64(p.PerPage) - 1) / int64(p.PerPage))
	if last < 1 {
		last = 1
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "meta": gin.H{"page": p.Page, "per_page": p.PerPage, "total": total, "last_page": last}})
}

func userID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		httpx.NotFound(c, "")
		return 0, false
	}
	return id, true
}

func (m *Module) loadUser(id int64) ([]byte, error) {
	out, err := m.jsonOne("SELECT "+detailJSON+" FROM users u WHERE u.id = ? AND u.deleted_at IS NULL", id)
	return out, err
}

func (m *Module) showUser(c *gin.Context) {
	id, ok := userID(c)
	if !ok {
		return
	}
	out, err := m.loadUser(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	okJSON(c, out)
}

func (m *Module) assignRoles(tx *gorm.DB, uid int64, roles []string) error {
	if err := tx.Exec("DELETE FROM user_roles WHERE user_id = ?", uid).Error; err != nil {
		return err
	}
	if len(roles) == 0 {
		return nil
	}
	return tx.Exec("INSERT INTO user_roles (user_id, role_id) SELECT ?, id FROM roles WHERE name IN ? ON CONFLICT DO NOTHING", uid, roles).Error
}

func (m *Module) validRoles(roles []string) (bool, error) {
	if len(roles) == 0 {
		return true, nil
	}
	var n int64
	err := m.db.Raw("SELECT count(*) FROM roles WHERE name IN ?", roles).Scan(&n).Error
	return n == int64(len(roles)), err
}

func (m *Module) createUser(c *gin.Context) {
	var b struct {
		Name        string   `json:"name" binding:"required,max=255"`
		Email       string   `json:"email" binding:"required,email,max=255"`
		Roles       []string `json:"roles" binding:"required,min=1,max=20"`
		UserType    string   `json:"user_type"`
		StudentCode string   `json:"student_code" binding:"max=50"`
		StaffCode   string   `json:"staff_code" binding:"max=50"`
		Phone       string   `json:"phone" binding:"max=32"`
		Password    string   `json:"password"`
		SendInvite  bool     `json:"send_invite"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	verr := httpx.NewValidationError()
	if b.UserType == "" {
		b.UserType = "student"
	}
	if !userTypes[b.UserType] {
		verr.Add("user_type", "The selected user type is invalid.")
	}
	if ok, err := m.validRoles(b.Roles); err != nil {
		httpx.Fail(c, err)
		return
	} else if !ok {
		verr.Add("roles", "The selected roles are invalid.")
	}
	if !b.SendInvite && b.Password != "" && (len(b.Password) < 8 || len(b.Password) > 72) {
		verr.Add("password", "The password must be between 8 and 72 characters.")
	}
	var dup int64
	_ = m.db.Raw("SELECT count(*) FROM users WHERE email = ?", strings.TrimSpace(b.Email)).Scan(&dup)
	if dup > 0 {
		verr.Add("email", "The email has already been taken.")
	}
	if verr.HasErrors() {
		httpx.Fail(c, verr)
		return
	}
	pw := b.Password
	if pw == "" || b.SendInvite {
		pw = randomPassword()
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var id int64
	err = m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`INSERT INTO users (name, email, password_hash, user_type, student_code, staff_code, phone, email_verified_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, now()) RETURNING id`, strings.TrimSpace(b.Name), strings.TrimSpace(b.Email), hash, b.UserType,
			nullable(b.StudentCode), nullable(b.StaffCode), nullable(b.Phone)).Scan(&id).Error; err != nil {
			return err
		}
		return m.assignRoles(tx, id, b.Roles)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+userJSON+" FROM users u WHERE u.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Data(http.StatusCreated, "application/json; charset=utf-8", out)
}

func (m *Module) updateUser(c *gin.Context) {
	id, ok := userID(c)
	if !ok {
		return
	}
	var b struct {
		Name          string `json:"name" binding:"required,max=255"`
		Email         string `json:"email" binding:"required,email,max=255"`
		UserType      string `json:"user_type" binding:"required"`
		Phone         string `json:"phone"`
		StudentCode   string `json:"student_code"`
		StaffCode     string `json:"staff_code"`
		Gender        string `json:"gender"`
		DateOfBirth   string `json:"date_of_birth"`
		Hometown      string `json:"hometown"`
		PermanentAddr string `json:"permanent_address"`
		Nationality   string `json:"nationality"`
		IDCardNumber  string `json:"id_card_number"`
		Bio           string `json:"bio"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	verr := httpx.NewValidationError()
	if !userTypes[b.UserType] {
		verr.Add("user_type", "The selected user type is invalid.")
	}
	if b.Gender != "" && b.Gender != "male" && b.Gender != "female" && b.Gender != "other" {
		verr.Add("gender", "The selected gender is invalid.")
	}
	var dup int64
	_ = m.db.Raw("SELECT count(*) FROM users WHERE email = ? AND id <> ?", strings.TrimSpace(b.Email), id).Scan(&dup)
	if dup > 0 {
		verr.Add("email", "The email has already been taken.")
	}
	if verr.HasErrors() {
		httpx.Fail(c, verr)
		return
	}
	nat := strings.TrimSpace(b.Nationality)
	if nat == "" {
		nat = "Việt Nam"
	}
	res := m.db.Exec(`UPDATE users SET name = ?, email = ?, user_type = ?, phone = ?, student_code = ?, staff_code = ?, gender = ?,
		date_of_birth = ?, hometown = ?, permanent_address = ?, nationality = ?, id_card_number = ?, bio = ? WHERE id = ? AND deleted_at IS NULL`,
		strings.TrimSpace(b.Name), strings.TrimSpace(b.Email), b.UserType, nullable(b.Phone), nullable(b.StudentCode), nullable(b.StaffCode),
		nullable(b.Gender), nullable(b.DateOfBirth), nullable(b.Hometown), nullable(b.PermanentAddr), nat, nullable(b.IDCardNumber), nullable(b.Bio), id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	out, err := m.loadUser(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	okJSON(c, out)
}

// guardSelf blocks admins from locking or deleting their own account.
func guardSelf(c *gin.Context, id int64) bool {
	if auth.UserID(c) == id {
		httpx.Abort(c, http.StatusUnprocessableEntity, "You cannot perform this action on your own account.")
		return false
	}
	return true
}

func (m *Module) deleteUser(c *gin.Context) {
	id, ok := userID(c)
	if !ok || !guardSelf(c, id) {
		return
	}
	res := m.db.Exec("UPDATE users SET deleted_at = now() WHERE id = ? AND deleted_at IS NULL", id)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) setDisabled(disable bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := userID(c)
		if !ok {
			return
		}
		if disable && !guardSelf(c, id) {
			return
		}
		expr := "NULL"
		if disable {
			expr = "now()"
		}
		res := m.db.Exec("UPDATE users SET disabled_at = "+expr+" WHERE id = ? AND deleted_at IS NULL", id)
		if res.Error != nil {
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			httpx.NotFound(c, "")
			return
		}
		out, err := m.jsonOne("SELECT "+userJSON+" FROM users u WHERE u.id = ?", id)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		okJSON(c, out)
	}
}

func (m *Module) setUserRoles(c *gin.Context) {
	id, ok := userID(c)
	if !ok {
		return
	}
	var b struct {
		Roles []string `json:"roles" binding:"required,min=1,max=20"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if good, err := m.validRoles(b.Roles); err != nil {
		httpx.Fail(c, err)
		return
	} else if !good {
		httpx.Fail(c, httpx.NewValidationError().Add("roles", "The selected roles are invalid."))
		return
	}
	hasAdmin := false
	for _, r := range b.Roles {
		hasAdmin = hasAdmin || r == auth.RoleAdmin
	}
	if auth.UserID(c) == id && !hasAdmin {
		httpx.Abort(c, http.StatusUnprocessableEntity, "You cannot remove your own admin role.")
		return
	}
	if err := m.db.Transaction(func(tx *gorm.DB) error { return m.assignRoles(tx, id, b.Roles) }); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+userJSON+" FROM users u WHERE u.id = ? AND u.deleted_at IS NULL", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	okJSON(c, out)
}

// resetPassword e-mails the user a link to choose a new password; no password is ever shown to the admin.
func (m *Module) resetPassword(c *gin.Context) {
	id, ok := userID(c)
	if !ok {
		return
	}
	var n int64
	if err := m.db.Raw("SELECT count(*) FROM users WHERE id = ? AND deleted_at IS NULL", id).Scan(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return
	}
	if err := m.resetter.SendPasswordReset(c.Request.Context(), id); err != nil {
		httpx.Abort(c, http.StatusBadGateway, "Không gửi được email đặt lại mật khẩu. Hãy kiểm tra cấu hình email (SMTP) rồi thử lại.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Đã gửi email đặt lại mật khẩu cho người dùng."})
}

func (m *Module) bulkUsers(c *gin.Context) {
	var b struct {
		Action string  `json:"action" binding:"required,oneof=disable enable delete"`
		IDs    []int64 `json:"ids" binding:"required,min=1,max=200"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	self := auth.UserID(c)
	ids := make([]int64, 0, len(b.IDs))
	for _, id := range b.IDs {
		if id != self || b.Action == "enable" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"affected": 0})
		return
	}
	var q string
	switch b.Action {
	case "disable":
		q = "UPDATE users SET disabled_at = now() WHERE id IN ? AND deleted_at IS NULL"
	case "enable":
		q = "UPDATE users SET disabled_at = NULL WHERE id IN ? AND deleted_at IS NULL"
	default:
		q = "UPDATE users SET deleted_at = now() WHERE id IN ? AND deleted_at IS NULL"
	}
	res := m.db.Exec(q, ids)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	c.JSON(http.StatusOK, gin.H{"affected": res.RowsAffected})
}
