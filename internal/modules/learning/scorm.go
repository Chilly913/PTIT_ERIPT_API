package learning

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// SCORM 1.2 / 2004 packages: instructors upload a .zip, the server unpacks it under UPLOAD_DIR/scorm/<uuid>/,
// reads imsmanifest.xml for the launch file and version, and serves the content at /api/scorm/<uuid>/...
// (reached by the web app as /bff/scorm/...). Runtime results are posted to .../scorm/track.

const (
	maxScormZip      = 300 << 20
	maxScormUnpacked = 800 << 20
	maxScormFiles    = 8000
)

type manifest struct {
	Metadata struct {
		SchemaVersion string `xml:"schemaversion"`
	} `xml:"metadata"`
	Organizations struct {
		Default string `xml:"default,attr"`
		Org     []struct {
			ID    string `xml:"identifier,attr"`
			Title string `xml:"title"`
			Items []struct {
				Ref string `xml:"identifierref,attr"`
			} `xml:"item"`
		} `xml:"organization"`
	} `xml:"organizations"`
	Resources struct {
		Base string `xml:"base,attr"`
		Res  []struct {
			ID   string `xml:"identifier,attr"`
			Href string `xml:"href,attr"`
			Base string `xml:"base,attr"`
		} `xml:"resource"`
	} `xml:"resources"`
}

var versionRe = regexp.MustCompile(`(?i)2004|cam 1\.3`)

// readManifest returns (version, launch file, title) of an unpacked package.
func readManifest(dir string) (version, entry, title string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, "imsmanifest.xml"))
	if err != nil {
		return "", "", "", fmt.Errorf("Không tìm thấy imsmanifest.xml ở thư mục gốc của gói SCORM.")
	}
	var mf manifest
	if err := xml.Unmarshal(data, &mf); err != nil {
		return "", "", "", fmt.Errorf("Tệp imsmanifest.xml bị lỗi định dạng.")
	}
	version = "1.2"
	if versionRe.MatchString(mf.Metadata.SchemaVersion) {
		version = "2004"
	}
	hrefs := map[string]string{}
	first := ""
	for _, r := range mf.Resources.Res {
		if r.Href == "" {
			continue
		}
		h := mf.Resources.Base + r.Base + r.Href
		hrefs[r.ID] = h
		if first == "" {
			first = h
		}
	}
	for _, o := range mf.Organizations.Org {
		if mf.Organizations.Default != "" && o.ID != mf.Organizations.Default {
			continue
		}
		if title == "" {
			title = strings.TrimSpace(o.Title)
		}
		for _, it := range o.Items {
			if h, ok := hrefs[it.Ref]; ok && entry == "" {
				entry = h
			}
		}
	}
	if entry == "" {
		entry = first
	}
	if entry == "" {
		return "", "", "", fmt.Errorf("Gói SCORM không có tệp khởi chạy (resource href).")
	}
	return version, strings.TrimLeft(entry, "/"), title, nil
}

// unzipSafe extracts zr into dir, refusing path traversal and oversized archives.
func unzipSafe(zr *zip.Reader, dir string) error {
	if len(zr.File) > maxScormFiles {
		return fmt.Errorf("Gói SCORM có quá nhiều tệp.")
	}
	base, _ := filepath.Abs(dir)
	var total int64
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		target := filepath.Join(base, name)
		if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
			return fmt.Errorf("Gói SCORM chứa đường dẫn không hợp lệ.")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		total += int64(f.UncompressedSize64)
		if total > maxScormUnpacked {
			return fmt.Errorf("Gói SCORM sau khi giải nén quá lớn.")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}
		_, cerr := io.Copy(out, io.LimitReader(rc, maxScormUnpacked))
		out.Close()
		rc.Close()
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (m *Module) uploadScorm(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	lid, ok := idParam(c, "lid")
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("lessons").Where("id = ? AND course_id = ?", lid, cr.ID).Count(&n).Error; err != nil || n == 0 {
		httpx.NotFound(c, "")
		return
	}
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Hãy chọn tệp .zip của gói SCORM."))
		return
	}
	if !strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") || fh.Size > maxScormZip {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Chỉ nhận tệp .zip tối đa 300 MB."))
		return
	}
	f, err := fh.Open()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Không đọc được tệp .zip."))
		return
	}
	uuid := newUUID()
	dir := filepath.Join(storageRoot(), "scorm", uuid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpx.Fail(c, err)
		return
	}
	fail := func(msg string) {
		_ = os.RemoveAll(dir)
		httpx.Fail(c, httpx.NewValidationError().Add("file", msg))
	}
	if err := unzipSafe(zr, dir); err != nil {
		fail(err.Error())
		return
	}
	version, entry, title, err := readManifest(dir)
	if err != nil {
		fail(err.Error())
		return
	}
	if _, serr := os.Stat(filepath.Join(dir, filepath.FromSlash(entry))); serr != nil {
		fail("Tệp khởi chạy " + entry + " không có trong gói.")
		return
	}
	if title == "" {
		title = strings.TrimSuffix(fh.Filename, filepath.Ext(fh.Filename))
	}
	var oldUUID string
	_ = m.db.Raw("SELECT uuid::text FROM scorm_packages WHERE lesson_id = ?", lid).Scan(&oldUUID)
	err = m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO scorm_packages (lesson_id, uuid, version, entry_path, title) VALUES (?, ?::uuid, ?, ?, ?)
			ON CONFLICT (lesson_id) DO UPDATE SET uuid = EXCLUDED.uuid, version = EXCLUDED.version, entry_path = EXCLUDED.entry_path, title = EXCLUDED.title`,
			lid, uuid, version, entry, title).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE lessons SET type = 'scorm' WHERE id = ?", lid).Error
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		httpx.Fail(c, err)
		return
	}
	if oldUUID != "" && oldUUID != uuid {
		_ = os.RemoveAll(filepath.Join(storageRoot(), "scorm", oldUUID))
	}
	out, err := m.lessonByID(cr.ID, lid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"lesson": out})
}

// serveScorm serves unpacked package files. The folder name is an unguessable UUID.
func (m *Module) serveScorm(c *gin.Context) {
	uuid := c.Param("uuid")
	if len(uuid) != 36 || strings.ContainsAny(uuid, `/\.`) {
		httpx.NotFound(c, "")
		return
	}
	rel := path.Clean("/" + c.Param("filepath"))
	root, _ := filepath.Abs(filepath.Join(storageRoot(), "scorm", uuid))
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		httpx.NotFound(c, "")
		return
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		httpx.NotFound(c, "")
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		httpx.NotFound(c, "")
		return
	}
	defer f.Close()
	c.Header("Cache-Control", "public, max-age=300")
	// ServeContent (unlike ServeFile) does not redirect "/index.html" URLs.
	http.ServeContent(c.Writer, c.Request, filepath.Base(abs), st.ModTime(), f)
}

var (
	lessonStatuses = map[string]bool{"completed": true, "passed": true, "failed": true, "browsed": true, "incomplete": true}
	completions    = map[string]bool{"completed": true, "incomplete": true}
	successes      = map[string]bool{"passed": true, "failed": true, "unknown": true}
)

func (m *Module) trackScorm(c *gin.Context) {
	cr, lid, ok := m.courseLesson(c)
	if !ok {
		return
	}
	uid, ok := m.canLearn(c, cr)
	if !ok || !m.gate(c, cr, uid, lid) {
		return
	}
	var b struct {
		Version          string   `json:"version"`
		Status           string   `json:"status"`
		CompletionStatus string   `json:"completion_status"`
		SuccessStatus    string   `json:"success_status"`
		Score            *float64 `json:"score"`
		LessonLocation   string   `json:"lesson_location"`
		SuspendData      string   `json:"suspend_data"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if b.Version != "2004" {
		b.Version = "1.2"
	}
	var status, completion, success any
	if lessonStatuses[b.Status] {
		status = b.Status
	}
	if completions[b.CompletionStatus] {
		completion = b.CompletionStatus
	}
	if successes[b.SuccessStatus] {
		success = b.SuccessStatus
	}
	var score any
	if b.Score != nil && *b.Score >= 0 && *b.Score <= 100 {
		score = *b.Score
	}
	if len(b.SuspendData) > 262144 {
		b.SuspendData = b.SuspendData[:262144]
	}
	err := m.db.Exec(`INSERT INTO scorm_runtime_states (user_id, lesson_id, scorm_version, lesson_status, completion_status, success_status, score, lesson_location, suspend_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))
		ON CONFLICT (user_id, lesson_id) DO UPDATE SET scorm_version = EXCLUDED.scorm_version,
			lesson_status = coalesce(EXCLUDED.lesson_status, scorm_runtime_states.lesson_status),
			completion_status = coalesce(EXCLUDED.completion_status, scorm_runtime_states.completion_status),
			success_status = coalesce(EXCLUDED.success_status, scorm_runtime_states.success_status),
			score = coalesce(EXCLUDED.score, scorm_runtime_states.score),
			lesson_location = coalesce(EXCLUDED.lesson_location, scorm_runtime_states.lesson_location),
			suspend_data = coalesce(EXCLUDED.suspend_data, scorm_runtime_states.suspend_data)`,
		uid, lid, b.Version, status, completion, success, score, b.LessonLocation, b.SuspendData).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	done := b.Status == "completed" || b.Status == "passed" || b.CompletionStatus == "completed" || b.SuccessStatus == "passed"
	if done {
		if err := m.db.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, progress_percent, completed_at, last_watched_at)
			VALUES (?, ?, 100, now(), now())
			ON CONFLICT (user_id, lesson_id) DO UPDATE SET progress_percent = 100, completed_at = coalesce(lesson_progress.completed_at, now()), last_watched_at = now()`, uid, lid).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "completed": done})
}
