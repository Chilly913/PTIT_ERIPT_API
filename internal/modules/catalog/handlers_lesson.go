package catalog

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// lessonTypes is the LessonController validator list (= ck_lessons_type).
var lessonTypes = []string{"video", "audio", "file", "page", "quiz", "assignment", "forum", "survey", "zoom", "meet",
	"document", "virtual_class", "scorm", "h5p", "live", "offline"}

// videoExtensions are the formats accepted by upload-video.
var videoExtensions = []string{"mp4", "mov", "avi", "webm", "mkv", "m4v"}

const maxVideoKB = 1024000

// listLessons is GET /courses/{course}/lessons: full lessons for the owner,
// admins and enrolled learners, a locked outline (no video_url) for others.
func (m *Module) listLessons(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	owner, enrolled, err := m.lessonViewer(c, course)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.lessonsOf(ctx, "l.course_id = ?", course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !owner && !enrolled {
		out := make([]lessonLocked, len(rows))
		for i, r := range rows {
			out[i] = lessonLockedOf(r)
		}
		httpx.OK(c, out)
		return
	}
	var atts map[int64][]attachmentJSON
	withAtts := phpBool(c.Query("include_attachments"))
	if withAtts {
		if atts, err = m.attachmentsOf(ctx, lessonIDs(rows)); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	out := make([]lessonJSON, len(rows))
	for i, r := range rows {
		out[i] = m.lessonJSON(r)
		if withAtts {
			a := atts[r.ID]
			if a == nil {
				a = []attachmentJSON{}
			}
			out[i].Attachments = &a
		}
	}
	httpx.OK(c, out)
}

// canWatch is the show/video-url guard: preview lessons are public, others
// need owner/admin or an enrollment (403 "Enrollment required").
func (m *Module) canWatch(c *gin.Context, course learning.CourseRef, lesson learning.LessonRef) (owner bool, ok bool) {
	owner, enrolled, err := m.lessonViewer(c, course)
	if err != nil {
		httpx.Fail(c, err)
		return false, false
	}
	if !lesson.IsPreview && !owner && !enrolled {
		httpx.Forbidden(c, "Enrollment required")
		return owner, false
	}
	return owner, true
}

// showLesson is GET /courses/{course}/lessons/{lesson} with every content relation.
func (m *Module) showLesson(c *gin.Context) {
	ctx := c.Request.Context()
	course, ref, ok := lessonInCourse(c)
	if !ok {
		return
	}
	owner, ok := m.canWatch(c, course, ref)
	if !ok {
		return
	}
	row, err := m.findLesson(ctx, ref.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.lessonDetail(ctx, row, owner)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (m *Module) lessonDetail(ctx context.Context, row lessonRow, owner bool) (lessonDetail, error) {
	ids := []int64{row.ID}
	out := lessonDetail{lessonJSON: m.lessonJSON(row)}
	vcs, err := m.virtualClassesOf(ctx, ids)
	if err != nil {
		return out, err
	}
	scorms, err := m.scormPackagesOf(ctx, ids)
	if err != nil {
		return out, err
	}
	offline, err := m.offlineSessionsOf(ctx, ids)
	if err != nil {
		return out, err
	}
	assignments, err := m.assignmentsOf(ctx, ids)
	if err != nil {
		return out, err
	}
	atts, err := m.attachmentsOf(ctx, ids)
	if err != nil {
		return out, err
	}
	quizzes, err := m.quizzesOf(ctx, ids)
	if err != nil {
		return out, err
	}
	out.VirtualClass, out.ScormPackage, out.Assignment = vcs[row.ID], scorms[row.ID], assignments[row.ID]
	if s := offline[row.ID]; s != nil {
		if !owner {
			// Hardening: the attendance QR token would let a learner check in
			// without scanning the code in the room.
			s.QRToken, s.QRExpiresAt = nil, httpx.Time{}
		}
		out.OfflineSession = s
	}
	out.Attachments = atts[row.ID]
	if out.Attachments == nil {
		out.Attachments = []attachmentJSON{}
	}
	for i := range out.Attachments {
		fp := out.Attachments[i].FilePath
		out.Attachments[i].URL = m.media.Public(&fp)
	}
	if q := quizzes[row.ID]; q != nil {
		out.Quiz = quizOf(*q)
	}
	return out, nil
}

// videoURL is GET /courses/{course}/lessons/{lesson}/video-url.
func (m *Module) videoURL(c *gin.Context) {
	course, ref, ok := lessonInCourse(c)
	if !ok {
		return
	}
	if _, ok := m.canWatch(c, course, ref); !ok {
		return
	}
	if ref.VideoPath == nil || strings.TrimSpace(*ref.VideoPath) == "" {
		httpx.NotFound(c, "No video uploaded yet")
		return
	}
	httpx.OK(c, gin.H{
		"url":        m.media.Public(ref.VideoPath),
		"expires_at": m.now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05-07:00"),
		"expires_in": 3600,
	})
}

// ---------------------------------------------------------------------------
// lesson write

// sectionInCourse is `exists:sections,id`, restricted to the lesson's course
// (the composite FK lessons(section_id, course_id) would reject other sections).
func (m *Module) sectionInCourse(ctx context.Context, courseID int64) func(int64) (bool, error) {
	return func(id int64) (bool, error) {
		var n int64
		err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM sections WHERE id = ? AND course_id = ?`, id, courseID).Scan(&n).Error
		if err != nil {
			return false, fmtErr("section lookup", err)
		}
		return n > 0, nil
	}
}

// storeLesson is POST /courses/{course}/lessons.
func (m *Module) storeLesson(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("title").required().string().max(255)
	v.field("description").nullableRule().string()
	v.field("section_id").nullableRule().integer().exists(m.sectionInCourse(ctx, course.ID))
	v.field("type").nullableRule().string().in(lessonTypes...)
	v.field("is_preview").nullableRule().boolean()
	v.field("order").nullableRule().integer().min(0)
	v.field("duration").nullableRule().integer().min(0)
	v.field("video_url").nullableRule().string().max(2048)
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}

	typ := "video"
	if s := in.str("type"); s != "" {
		typ = s
	}
	var position int64
	if in.get("order") != nil {
		position = phpInt(in.get("order"))
	} else if err := m.db.WithContext(ctx).Raw(`SELECT coalesce(max(position), 0) + 1 FROM lessons WHERE course_id = ?`, course.ID).Scan(&position).Error; err != nil {
		httpx.Fail(c, fmtErr("max lesson order", err))
		return
	}
	var duration int64
	if in.get("duration") != nil {
		duration = phpInt(in.get("duration"))
	}
	var id int64
	err = m.db.WithContext(ctx).Raw(`INSERT INTO lessons
 (course_id, section_id, title, type, description, position, duration_seconds, is_preview, video_path)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		course.ID, nullableInt(in, "section_id"), in.str("title"), typ, nullableStr(in, "description"),
		position, duration, phpBool(in.get("is_preview")), m.videoKey(in),
	).Scan(&id).Error
	if err != nil {
		httpx.Fail(c, fmtErr("create lesson", err))
		return
	}
	row, err := m.findLesson(ctx, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, gin.H{"message": "Lesson created", "lesson": m.lessonJSON(row)})
}

// videoKey converts the video_url input to the stored video_path.
func (m *Module) videoKey(in *input) *string {
	s := in.str("video_url")
	if s == "" {
		return nil
	}
	k := m.media.StorageKey(s)
	return &k
}

// updateLesson is PUT /courses/{course}/lessons/{lesson}.
func (m *Module) updateLesson(c *gin.Context) {
	ctx := c.Request.Context()
	course, ref, ok := lessonInCourse(c)
	if !ok || !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("title").sometimes().string().max(255)
	v.field("description").nullableRule().string()
	v.field("section_id").nullableRule().integer().exists(m.sectionInCourse(ctx, course.ID))
	v.field("type").sometimes().string().in(lessonTypes...)
	v.field("is_preview").nullableRule().boolean()
	v.field("order").sometimes().integer().min(0)
	v.field("duration").sometimes().integer().min(0)
	v.field("video_url").nullableRule().string().max(2048)
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}

	set := map[string]any{}
	if in.has("title") {
		set["title"] = in.str("title")
	}
	if in.has("description") {
		set["description"] = nullableStr(in, "description")
	}
	if in.has("section_id") {
		set["section_id"] = nullableInt(in, "section_id")
	}
	if in.has("type") {
		set["type"] = in.str("type")
	}
	if in.has("is_preview") {
		set["is_preview"] = phpBool(in.get("is_preview")) // null -> false (column is NOT NULL)
	}
	if in.has("order") {
		set["position"] = phpInt(in.get("order"))
	}
	if in.has("duration") {
		set["duration_seconds"] = phpInt(in.get("duration"))
	}
	if in.has("video_url") {
		set["video_path"] = m.videoKey(in)
	}
	if err := m.updateRow(ctx, "lessons", ref.ID, set, nil); err != nil {
		httpx.Fail(c, err)
		return
	}
	row, err := m.findLesson(ctx, ref.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Lesson updated", "lesson": m.lessonJSON(row)})
}

// destroyLesson is DELETE /courses/{course}/lessons/{lesson}; the stored video
// object is removed too (MediaService::delete).
func (m *Module) destroyLesson(c *gin.Context) {
	ctx := c.Request.Context()
	course, ref, ok := lessonInCourse(c)
	if !ok || !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	if err := m.db.WithContext(ctx).Exec(`DELETE FROM lessons WHERE id = ?`, ref.ID).Error; err != nil {
		httpx.Fail(c, fmtErr("delete lesson", err))
		return
	}
	m.deleteObject(ctx, ref.VideoPath)
	httpx.Message(c, http.StatusOK, "Lesson deleted")
}

// deleteObject removes a stored media value when it is one of our object keys
// (external URLs and legacy public paths are left alone). Failures are logged.
func (m *Module) deleteObject(ctx context.Context, p *string) {
	if p == nil || m.store == nil {
		return
	}
	key := m.media.StorageKey(*p)
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "://") {
		return
	}
	if err := m.store.Delete(ctx, key); err != nil {
		logx.FromContext(ctx).WarnContext(ctx, "delete media object failed", "key", key, "error", err.Error())
	}
}

// uploadVideo is POST /courses/{course}/lessons/{lesson}/upload-video (multipart `video`).
func (m *Module) uploadVideo(c *gin.Context) {
	ctx := c.Request.Context()
	course, ref, ok := lessonInCourse(c)
	if !ok || !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("video").required().file().max(maxVideoKB)
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}
	fh := fileOf(in, "video")
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(fh.Filename), "."))
	allowed := false
	for _, e := range videoExtensions {
		allowed = allowed || e == ext
	}
	if !allowed {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"message": "Định dạng ." + ext + " không được hỗ trợ. Chỉ chấp nhận: mp4, mov, avi, webm, mkv, m4v.",
			"errors":  gin.H{"video": []string{"Định dạng ." + ext + " không hợp lệ."}},
		})
		return
	}
	key, err := m.putUpload(ctx, fh, "lessons/videos/"+m.now().UTC().Format("2006-01-02"))
	if err != nil {
		logx.FromContext(ctx).ErrorContext(ctx, "video upload failed", "error", err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Video upload failed", "error": err.Error()})
		return
	}
	if err := m.updateRow(ctx, "lessons", ref.ID, map[string]any{
		"video_path": key, "video_size_bytes": fh.Size, "video_status": "ready",
	}, nil); err != nil {
		httpx.Fail(c, err)
		return
	}
	if ref.VideoPath != nil && *ref.VideoPath != key {
		m.deleteObject(ctx, ref.VideoPath)
	}
	row, err := m.findLesson(ctx, ref.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Video uploaded successfully", "lesson": m.lessonJSON(row)})
}

// reorderLessons is POST /courses/{course}/lessons/reorder ({"order": [ids]}).
func (m *Module) reorderLessons(c *gin.Context) {
	ctx := c.Request.Context()
	course := boundCourse(c)
	if !m.ownerOnly(c, course, "Forbidden") {
		return
	}
	in, err := readInput(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v := newValidator(in)
	v.field("order").required().array()
	keys, vals := arrayItems(in.get("order"))
	for i, k := range keys {
		v.value("order."+k, vals[i], true).integer()
	}
	if err := v.err(); err != nil {
		httpx.Fail(c, err)
		return
	}
	err = m.db.WithContext(ctx).Transaction(func(tx *gormDB) error {
		for i, val := range vals {
			if err := tx.Exec(`UPDATE lessons SET position = ? WHERE id = ? AND course_id = ?`, i+1, phpInt(val), course.ID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, fmtErr("reorder lessons", err))
		return
	}
	httpx.Message(c, http.StatusOK, "Lessons reordered")
}
