package learning

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// QR attendance for in-person sessions. A session belongs to a course; its roster is the course enrolment.
// The instructor issues a short-lived QR token, students post it to /attendance/check-in.

const sessionJSON = `jsonb_build_object('id', o.id, 'title', o.title, 'course_id', o.course_id, 'course_title', c.title,
  'section_code', (SELECT s.code::text FROM class_sections s WHERE s.id = o.class_section_id),
  'location', o.location, 'room', o.room, 'start_at', o.start_at, 'end_at', o.start_at + make_interval(mins => o.duration_minutes),
  'duration_minutes', o.duration_minutes,
  'state', CASE WHEN now() < o.start_at THEN 'upcoming' WHEN now() > o.start_at + make_interval(mins => o.duration_minutes) THEN 'ended' ELSE 'running' END,
  'is_active', o.is_active, 'qr_enabled', o.qr_enabled, 'qr_mode', o.qr_mode, 'qr_rotate_seconds', o.qr_rotate_seconds,
  'check_in_radius_meters', o.check_in_radius_meters, 'latitude', o.latitude::float8, 'longitude', o.longitude::float8,
  'counts', jsonb_build_object(
     'present', (SELECT count(*) FROM offline_session_attendances a WHERE a.offline_session_id = o.id AND a.status = 'present'),
     'late', (SELECT count(*) FROM offline_session_attendances a WHERE a.offline_session_id = o.id AND a.status = 'late'),
     'excused', (SELECT count(*) FROM offline_session_attendances a WHERE a.offline_session_id = o.id AND a.status = 'excused'),
     'recorded', (SELECT count(*) FROM offline_session_attendances a WHERE a.offline_session_id = o.id)))`

const sessionFrom = `offline_sessions o LEFT JOIN courses c ON c.id = o.course_id`

func (m *Module) listSessions(c *gin.Context) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	page, per := pageParams(c, 12, 100)
	conds, args := []string{"o.course_id IS NOT NULL"}, []any{}
	if !hasRole(roles, auth.RoleAdmin) {
		conds = append(conds, "c.instructor_id = ?")
		args = append(args, uid)
	}
	if cid := c.Query("course_id"); cid != "" {
		conds = append(conds, "o.course_id = ?")
		args = append(args, cid)
	}
	w := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+sessionFrom+w, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+sessionJSON+" FROM "+sessionFrom+w+" ORDER BY o.start_at DESC, o.id DESC LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) createSession(c *gin.Context) {
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var b struct {
		CourseID  int64    `json:"course_id"`
		Title     *string  `json:"title"`
		Location  string   `json:"location"`
		StartAt   *string  `json:"start_at"`
		Duration  int      `json:"duration_minutes"`
		Radius    int      `json:"check_in_radius_meters"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		QrMode    string   `json:"qr_mode"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	v := httpx.NewValidationError()
	if b.QrMode == "" {
		b.QrMode = "manual"
	}
	if b.QrMode != "manual" && b.QrMode != "rotating" && b.QrMode != "static" {
		v.Add("qr_mode", "The selected qr mode is invalid.")
	}
	if b.CourseID == 0 || !m.canUseCourse(uid, roles, b.CourseID) {
		v.Add("course_id", "The selected course is invalid.")
	}
	if strings.TrimSpace(b.Location) == "" {
		v.Add("location", "Vui lòng nhập địa điểm.")
	}
	start := parseWhen(b.StartAt)
	if start == nil {
		v.Add("start_at", "Vui lòng chọn thời gian bắt đầu.")
	}
	if b.Duration < 1 || b.Duration > 1440 {
		v.Add("duration_minutes", "Thời lượng phải từ 1 đến 1440 phút.")
	}
	if b.Radius == 0 {
		b.Radius = 15
	}
	if b.Radius < 5 || b.Radius > 500 {
		v.Add("check_in_radius_meters", "Bán kính phải từ 5 đến 500 mét.")
	}
	if (b.Latitude == nil) != (b.Longitude == nil) || (b.Latitude != nil && (*b.Latitude < -90 || *b.Latitude > 90 || *b.Longitude < -180 || *b.Longitude > 180)) {
		v.Add("latitude", "Toạ độ không hợp lệ.")
	}
	if v.HasErrors() {
		httpx.Fail(c, v)
		return
	}
	var id int64
	err = m.db.Raw(`INSERT INTO offline_sessions (course_id, title, location, start_at, duration_minutes, check_in_radius_meters, latitude, longitude, is_active, qr_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, true, ?) RETURNING id`, b.CourseID, nullStr(b.Title), strings.TrimSpace(b.Location), start, b.Duration, b.Radius, b.Latitude, b.Longitude, b.QrMode).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+sessionJSON+" FROM "+sessionFrom+" WHERE o.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

// ownedSession resolves :id and requires the caller to own the session's course.
func (m *Module) ownedSession(c *gin.Context) (id, courseID int64, ok bool) {
	id, ok = idParam(c, "id")
	if !ok {
		return
	}
	var row struct{ CourseID *int64 }
	res := m.db.Raw("SELECT course_id FROM offline_sessions WHERE id = ?", id).Scan(&row)
	cid := row.CourseID
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return 0, 0, false
	}
	if res.RowsAffected == 0 || cid == nil {
		httpx.NotFound(c, "")
		return 0, 0, false
	}
	uid, roles, err := m.actor(c)
	if err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	if !m.canUseCourse(uid, roles, *cid) {
		httpx.Forbidden(c, "")
		return 0, 0, false
	}
	return id, *cid, true
}

func (m *Module) showSession(c *gin.Context) {
	id, _, ok := m.ownedSession(c)
	if !ok {
		return
	}
	out, err := m.jsonOne("SELECT "+sessionJSON+" FROM "+sessionFrom+" WHERE o.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) deleteSession(c *gin.Context) {
	id, _, ok := m.ownedSession(c)
	if !ok {
		return
	}
	if err := m.db.Exec("DELETE FROM offline_sessions WHERE id = ?", id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) issueQR(c *gin.Context) {
	id, _, ok := m.ownedSession(c)
	if !ok {
		return
	}
	var s struct {
		QrEnabled bool
		QrMode    string
		Rotate    int
		StartAt   time.Time
		Minutes   int
	}
	if err := m.db.Raw("SELECT qr_enabled, qr_mode, qr_rotate_seconds AS rotate, start_at, duration_minutes AS minutes FROM offline_sessions WHERE id = ?", id).Scan(&s).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	end := s.StartAt.Add(time.Duration(s.Minutes) * time.Minute)
	if !s.QrEnabled || time.Now().After(end) {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Buổi học đã kết thúc hoặc không bật điểm danh bằng QR.")
		return
	}
	// Rotating codes live for qr_rotate_seconds (default 60); the others until the session ends.
	ttl := time.Duration(s.Rotate) * time.Second
	if s.QrMode == "static" {
		ttl = time.Until(end)
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	exp := time.Now().Add(ttl)
	if err := m.db.Exec("UPDATE offline_sessions SET qr_token = ?, qr_expires_at = ?, is_active = true WHERE id = ?", token, exp, id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token, "payload": fmt.Sprintf("eript-attendance:%d:%s", id, token),
		"expires_at": exp, "ttl_seconds": int(ttl.Seconds()), "mode": s.QrMode,
	})
}

func (m *Module) sessionRoster(c *gin.Context) {
	id, courseID, ok := m.ownedSession(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 20, 100)
	conds, args := []string{"e.course_id = ?"}, []any{courseID}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "(u.name ILIKE ? OR u.email::text ILIKE ? OR u.student_code::text ILIKE ?)")
		args = append(args, likeArg(s), likeArg(s), likeArg(s))
	}
	status := "coalesce(a.status, 'absent')"
	if st := c.Query("status"); st != "" {
		conds = append(conds, status+" = ?")
		args = append(args, st)
	}
	from := `enrollments e JOIN users u ON u.id = e.user_id LEFT JOIN offline_session_attendances a ON a.offline_session_id = ` + fmt.Sprint(id) + ` AND a.user_id = u.id WHERE ` + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM "+from, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('user_id', u.id, 'name', u.name, 'student_code', u.student_code, 'email', u.email,
		'status', `+status+`, 'recorded', a.id IS NOT NULL, 'checked_in_at', a.checked_in_at, 'distance_meters', a.distance_meters::float8)
		FROM `+from+` ORDER BY u.name LIMIT ? OFFSET ?`, append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) markAttendance(c *gin.Context) {
	id, courseID, ok := m.ownedSession(c)
	if !ok {
		return
	}
	uid, ok := idParam(c, "uid")
	if !ok {
		return
	}
	var b struct {
		Status string `json:"status"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if b.Status != "present" && b.Status != "late" && b.Status != "absent" && b.Status != "excused" {
		httpx.Fail(c, httpx.NewValidationError().Add("status", "The selected status is invalid."))
		return
	}
	if isEnrolled, err := m.enrolled(uid, courseID); err != nil {
		httpx.Fail(c, err)
		return
	} else if !isEnrolled {
		httpx.NotFound(c, "")
		return
	}
	err := m.db.Exec(`INSERT INTO offline_session_attendances (offline_session_id, user_id, status, checked_in_at) VALUES (?, ?, ?, CASE WHEN ? IN ('present','late') THEN now() END)
		ON CONFLICT (offline_session_id, user_id) DO UPDATE SET status = EXCLUDED.status,
			checked_in_at = coalesce(offline_session_attendances.checked_in_at, EXCLUDED.checked_in_at)`, id, uid, b.Status, b.Status).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user_id": uid, "status": b.Status})
}

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// checkIn is called by a student after scanning the QR code.
func (m *Module) checkIn(c *gin.Context) {
	uid := auth.UserID(c)
	var b struct {
		Payload   string   `json:"payload"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		Device    string   `json:"device_info"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	var sid int64
	var token string
	if _, err := fmt.Sscanf(strings.Replace(b.Payload, ":", " ", 2), "eript-attendance %d %s", &sid, &token); err != nil || sid == 0 || token == "" {
		httpx.Fail(c, httpx.NewValidationError().Add("payload", "Mã QR không hợp lệ."))
		return
	}
	var s struct {
		CourseID  *int64
		QrToken   *string
		ExpiresAt *time.Time
		StartAt   time.Time
		Minutes   int
		Radius    int
		Lat, Lon  *float64
	}
	res := m.db.Raw(`SELECT course_id, qr_token, qr_expires_at AS expires_at, start_at, duration_minutes AS minutes, check_in_radius_meters AS radius,
		latitude::float8 AS lat, longitude::float8 AS lon FROM offline_sessions WHERE id = ? AND qr_enabled`, sid).Scan(&s)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 || s.QrToken == nil || *s.QrToken != token {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Mã QR không đúng. Hãy quét mã mới nhất.")
		return
	}
	if s.ExpiresAt == nil || time.Now().After(*s.ExpiresAt) {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Mã QR đã hết hạn. Hãy quét mã mới.")
		return
	}
	if s.CourseID == nil {
		httpx.NotFound(c, "")
		return
	}
	if isEnrolled, err := m.enrolled(uid, *s.CourseID); err != nil {
		httpx.Fail(c, err)
		return
	} else if !isEnrolled {
		httpx.Forbidden(c, "Bạn không thuộc khoá học của buổi học này.")
		return
	}
	var dist *float64
	if s.Lat != nil && s.Lon != nil {
		if b.Latitude == nil || b.Longitude == nil {
			httpx.Abort(c, http.StatusUnprocessableEntity, "Buổi học yêu cầu vị trí. Hãy bật định vị và thử lại.")
			return
		}
		d := haversine(*s.Lat, *s.Lon, *b.Latitude, *b.Longitude)
		if d > float64(s.Radius) {
			httpx.Abort(c, http.StatusUnprocessableEntity, fmt.Sprintf("Bạn ở quá xa lớp học (%.0f m, cho phép %d m).", d, s.Radius))
			return
		}
		dist = &d
	}
	status := "present"
	if time.Now().After(s.StartAt.Add(15 * time.Minute)) {
		status = "late"
	}
	err := m.db.Exec(`INSERT INTO offline_session_attendances (offline_session_id, user_id, status, checked_in_at, latitude, longitude, distance_meters, device_info)
		VALUES (?, ?, ?, now(), ?, ?, ?, NULLIF(?, ''))
		ON CONFLICT (offline_session_id, user_id) DO UPDATE SET status = EXCLUDED.status, checked_in_at = now(),
			latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude, distance_meters = EXCLUDED.distance_meters, device_info = EXCLUDED.device_info`,
		sid, uid, status, b.Latitude, b.Longitude, dist, b.Device).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": status, "message": "Điểm danh thành công."})
}
