package study

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

const noteCols = `id, user_id, lesson_id, content, position_seconds, created_at, updated_at`

// listNotes is GET .../notes: the caller's notes on the lesson, newest first.
func (m *Module) listNotes(c *gin.Context) {
	cl, _, lesson, ok := m.lessonGate(c)
	if !ok {
		return
	}
	var rows []noteRow
	err := m.db.WithContext(c.Request.Context()).Raw(`SELECT `+noteCols+` FROM lesson_notes
		WHERE user_id = ? AND lesson_id = ? ORDER BY created_at DESC, id DESC`, cl.UserID, lesson.ID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: list notes: %w", err))
		return
	}
	out := make([]noteJSON, 0, len(rows))
	for _, n := range rows {
		out = append(out, n.JSON())
	}
	httpx.OK(c, out)
}

// createNote is POST .../notes.
func (m *Module) createNote(c *gin.Context) {
	cl, _, lesson, ok := m.lessonGate(c)
	if !ok {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	content := r.stringField("content", true, false, 5000)
	pos := r.intField("position_seconds", false, i64(0), i64(maxInt32))
	legacyPos := r.intField("time_seconds", false, i64(0), i64(maxInt32)) // mobile clients
	if !r.ok(c) {
		return
	}
	position := int64(0)
	switch {
	case pos != nil:
		position = *pos
	case legacyPos != nil:
		position = *legacyPos
	}
	var rows []noteRow
	err := m.db.WithContext(c.Request.Context()).Raw(`INSERT INTO lesson_notes (user_id, lesson_id, content, position_seconds)
		VALUES (?, ?, ?, ?) RETURNING `+noteCols, cl.UserID, lesson.ID, *content, position).Scan(&rows).Error
	if err == nil && len(rows) == 0 {
		err = errors.New("insert returned no row")
	}
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: create note: %w", err))
		return
	}
	httpx.Created(c, gin.H{"message": "Lesson note created successfully.", "note": rows[0].JSON()})
}

// bindNote resolves {note} by id after the course/lesson gate, then applies
// authorizeOwnedNote (403 "Forbidden" for another lesson or user).
func (m *Module) bindNote(c *gin.Context) (noteRow, bool) {
	course, lesson, ok := m.bindCourseLesson(c)
	if !ok {
		return noteRow{}, false
	}
	key := c.Param("note")
	var rows []noteRow
	if id, ok := parseID(key); ok {
		if err := m.db.WithContext(c.Request.Context()).Raw(`SELECT `+noteCols+` FROM lesson_notes WHERE id = ?`, id).
			Scan(&rows).Error; err != nil {
			httpx.Fail(c, fmt.Errorf("study: find note: %w", err))
			return noteRow{}, false
		}
	}
	if len(rows) == 0 {
		modelNotFound(c, "LessonNote", key)
		return noteRow{}, false
	}
	note := rows[0]
	cl, ok := m.caller(c)
	if !ok || !requireLessonInCourse(c, course, lesson) || !m.requireAccess(c, cl, course) {
		return note, false
	}
	if note.LessonID != lesson.ID || note.UserID != cl.UserID {
		httpx.Abort(c, http.StatusForbidden, msgForbiddenPlain)
		return note, false
	}
	return note, true
}

// updateNote is PUT .../notes/{note}.
func (m *Module) updateNote(c *gin.Context) {
	note, ok := m.bindNote(c)
	if !ok {
		return
	}
	in, ok := readInput(c)
	if !ok {
		return
	}
	r := newRules(in)
	content := r.stringField("content", false, true, 5000)
	pos := r.intField("position_seconds", false, i64(0), i64(maxInt32))
	if !r.ok(c) {
		return
	}
	if content != nil {
		note.Content = *content
	}
	// An explicit null position_seconds would violate NOT NULL (legacy 500);
	// it leaves the stored value unchanged instead.
	if pos != nil {
		note.PositionSeconds = int(*pos)
	}
	var rows []noteRow
	err := m.db.WithContext(c.Request.Context()).Raw(`UPDATE lesson_notes SET content = ?, position_seconds = ?
		WHERE id = ? RETURNING `+noteCols, note.Content, note.PositionSeconds, note.ID).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, fmt.Errorf("study: update note: %w", err))
		return
	}
	if len(rows) > 0 {
		note = rows[0]
	}
	httpx.OK(c, gin.H{"message": "Lesson note updated successfully.", "note": note.JSON()})
}

// deleteNote is DELETE .../notes/{note}.
func (m *Module) deleteNote(c *gin.Context) {
	note, ok := m.bindNote(c)
	if !ok {
		return
	}
	if err := m.db.WithContext(c.Request.Context()).Exec(`DELETE FROM lesson_notes WHERE id = ?`, note.ID).Error; err != nil {
		httpx.Fail(c, fmt.Errorf("study: delete note: %w", err))
		return
	}
	httpx.OK(c, gin.H{"message": "Lesson note deleted successfully."})
}
