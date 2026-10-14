package study

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

const attachmentCols = `id, lesson_id, original_name, file_path, size_bytes, mime_type, created_at, updated_at`

// listAttachments is GET .../attachments. Legacy checks only that the lesson
// belongs to the course (no enrollment check).
func (m *Module) listAttachments(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok || !requireLessonInCourse(c, course, lesson) {
		return
	}
	var rows []attachmentRow
	err := m.db.WithContext(c.Request.Context()).Raw(`SELECT `+attachmentCols+` FROM lesson_attachments
		WHERE lesson_id = ? ORDER BY id`, lesson.ID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: list attachments: %w", err))
		return
	}
	out := make([]attachmentJSON, 0, len(rows))
	for _, a := range rows {
		out = append(out, m.attachmentJSON(a))
	}
	httpx.OK(c, gin.H{"attachments": out})
}

// bindAttachment resolves {attachment} by id (after course and lesson).
func (m *Module) bindAttachment(c *gin.Context) (attachmentRow, bool) {
	key := c.Param("attachment")
	var rows []attachmentRow
	if id, ok := parseID(key); ok {
		if err := m.db.WithContext(c.Request.Context()).Raw(`SELECT `+attachmentCols+` FROM lesson_attachments WHERE id = ?`, id).
			Scan(&rows).Error; err != nil {
			httpx.Fail(c, fmt.Errorf("study: find attachment: %w", err))
			return attachmentRow{}, false
		}
	}
	if len(rows) == 0 {
		modelNotFound(c, "LessonAttachment", key)
		return attachmentRow{}, false
	}
	return rows[0], true
}

// downloadAttachment is GET .../attachments/{attachment}/download → {"url"}.
func (m *Module) downloadAttachment(c *gin.Context) {
	_, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	a, ok := m.bindAttachment(c)
	if !ok {
		return
	}
	if a.LessonID != lesson.ID {
		httpx.Abort(c, http.StatusNotFound, msgNotFoundLower)
		return
	}
	httpx.OK(c, gin.H{"url": m.publicURL(a.FilePath)})
}

// uploadAttachment is POST .../attachments (multipart `file`, max 50 MB).
func (m *Module) uploadAttachment(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) || !requireLessonInCourse(c, course, lesson) {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	fh := r.fileField("file", maxUploadKB)
	if !r.ok(c) {
		return
	}
	up, err := m.upload(c, fh, fmt.Sprintf("courses/%d/lessons/%d/attachments", course.ID, lesson.ID))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var rows []attachmentRow
	err = m.db.WithContext(c.Request.Context()).Raw(`INSERT INTO lesson_attachments
			(lesson_id, original_name, file_path, size_bytes, mime_type)
		VALUES (?, ?, ?, ?, ?) RETURNING `+attachmentCols,
		lesson.ID, up.name, up.key, up.size, up.mime).Scan(&rows).Error
	if err == nil && len(rows) == 0 {
		err = errors.New("insert returned no row")
	}
	if err != nil {
		m.discard(c, up.key)
		httpx.Fail(c, fmt.Errorf("study: create attachment: %w", err))
		return
	}
	httpx.Created(c, gin.H{"message": "Attachment uploaded successfully", "attachment": m.attachmentJSON(rows[0])})
}

// deleteAttachment is DELETE .../attachments/{attachment}.
func (m *Module) deleteAttachment(c *gin.Context) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return
	}
	a, ok := m.bindAttachment(c)
	if !ok {
		return
	}
	cl, ok := m.caller(c)
	if !ok || !requireOwner(c, cl, course) {
		return
	}
	if a.LessonID != lesson.ID || lesson.CourseID != course.ID {
		httpx.Abort(c, http.StatusNotFound, msgNotFoundLower)
		return
	}
	m.discard(c, a.FilePath)
	if err := m.db.WithContext(c.Request.Context()).Exec(`DELETE FROM lesson_attachments WHERE id = ?`, a.ID).Error; err != nil {
		httpx.Fail(c, fmt.Errorf("study: delete attachment: %w", err))
		return
	}
	httpx.OK(c, gin.H{"message": "Attachment deleted"})
}

// uploaded is MediaService::upload's result.
type uploaded struct {
	key  string
	name string
	size int64
	mime string
}

// upload stores the file as "<folder>/<unix>_<random10>.<ext>" (MediaService::upload).
func (m *Module) upload(c *gin.Context, fh *multipart.FileHeader, folder string) (uploaded, error) {
	if m.store == nil {
		return uploaded{}, errors.New("study: object storage is not configured")
	}
	f, err := fh.Open()
	if err != nil {
		return uploaded{}, fmt.Errorf("study: open upload: %w", err)
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return uploaded{}, fmt.Errorf("study: rewind upload: %w", err)
	}
	ext := strings.TrimPrefix(path.Ext(fh.Filename), ".")
	key := fmt.Sprintf("%s/%d_%s.%s", folder, m.now().Unix(), randomAlnum(10), ext)
	mt := detectMime(head[:n], ext)
	if err := m.store.Put(c.Request.Context(), key, f, fh.Size, mt); err != nil {
		return uploaded{}, err
	}
	return uploaded{key: key, name: fh.Filename, size: fh.Size, mime: mt}, nil
}

// detectMime approximates UploadedFile::getMimeType (finfo): content sniffing,
// with the extension deciding for containers and plain text that sniffing
// cannot tell apart (docx/xlsx/pptx are zip, csv/json are text).
func detectMime(head []byte, ext string) string {
	mt := http.DetectContentType(head)
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "application/octet-stream", "application/zip", "text/plain":
		if ext != "" {
			if byExt := mime.TypeByExtension("." + strings.ToLower(ext)); byExt != "" {
				if i := strings.IndexByte(byExt, ';'); i >= 0 {
					byExt = strings.TrimSpace(byExt[:i])
				}
				return byExt
			}
		}
	}
	return mt
}

// discard deletes a stored object (MediaService::delete); only keys inside our
// bucket are deleted, external URLs are left alone. Failures are logged.
func (m *Module) discard(c *gin.Context, stored string) {
	if m.store == nil || stored == "" {
		return
	}
	key := m.media.StorageKey(stored)
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "://") {
		return
	}
	ctx := c.Request.Context()
	if err := m.store.Delete(ctx, key); err != nil {
		logx.FromContext(ctx).WarnContext(ctx, "delete stored file failed", "key", key, "error", err.Error())
	}
}
