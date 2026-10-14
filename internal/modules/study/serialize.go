package study

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// phpFloat marshals like PHP's json_encode of a float (serialize_precision -1):
// integral values keep ".0" (round(50, 1) => 50.0).
type phpFloat float64

func (f phpFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return []byte("0"), nil
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return []byte(strconv.FormatFloat(v, 'f', 1, 64)), nil
	}
	return []byte(strconv.FormatFloat(v, 'f', -1, 64)), nil
}

// roundPercent is PHP round(done / total * 100, 1).
func roundPercent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(done)/float64(total)*100*10) / 10
}

func tptr(t *time.Time) httpx.Time {
	if t == nil {
		return httpx.Time{}
	}
	return httpx.NewTime(*t)
}

// ---------------------------------------------------------------------------
// lesson_progress

// progressRow is a lesson_progress row.
type progressRow struct {
	UserID          int64      `gorm:"column:user_id"`
	LessonID        int64      `gorm:"column:lesson_id"`
	ProgressPercent int        `gorm:"column:progress_percent"`
	LastPosition    int        `gorm:"column:last_position"`
	WatchedSeconds  int        `gorm:"column:watched_seconds"`
	CompletedAt     *time.Time `gorm:"column:completed_at"`
	LastWatchedAt   *time.Time `gorm:"column:last_watched_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

// progressJSON is the legacy LessonProgress model JSON. Legacy `id` has no
// column any more (composite PK) and is omitted; `completed` is derived from
// completed_at; `metadata` moved to scorm_runtime_states and is always null.
type progressJSON struct {
	UserID          int64      `json:"user_id"`
	LessonID        int64      `json:"lesson_id"`
	ProgressPercent int        `json:"progress_percent"`
	LastPosition    int        `json:"last_position"`
	Completed       bool       `json:"completed"`
	CompletedAt     httpx.Time `json:"completed_at"`
	WatchedSeconds  int        `json:"watched_seconds"`
	LastWatchedAt   httpx.Time `json:"last_watched_at"`
	Metadata        any        `json:"metadata"`
	CreatedAt       httpx.Time `json:"created_at"`
	UpdatedAt       httpx.Time `json:"updated_at"`
}

func (p progressRow) JSON() progressJSON {
	return progressJSON{
		UserID: p.UserID, LessonID: p.LessonID,
		ProgressPercent: p.ProgressPercent, LastPosition: p.LastPosition,
		Completed: p.CompletedAt != nil, CompletedAt: tptr(p.CompletedAt),
		WatchedSeconds: p.WatchedSeconds, LastWatchedAt: tptr(p.LastWatchedAt),
		CreatedAt: httpx.NewTime(p.CreatedAt), UpdatedAt: httpx.NewTime(p.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------
// lesson_notes

type noteRow struct {
	ID              int64     `gorm:"column:id" json:"id"`
	UserID          int64     `gorm:"column:user_id" json:"user_id"`
	LessonID        int64     `gorm:"column:lesson_id" json:"lesson_id"`
	Content         string    `gorm:"column:content" json:"content"`
	PositionSeconds int       `gorm:"column:position_seconds" json:"position_seconds"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"-"`
	UpdatedAt       time.Time `gorm:"column:updated_at" json:"-"`
}

type noteJSON struct {
	ID              int64      `json:"id"`
	UserID          int64      `json:"user_id"`
	LessonID        int64      `json:"lesson_id"`
	Content         string     `json:"content"`
	PositionSeconds int        `json:"position_seconds"`
	CreatedAt       httpx.Time `json:"created_at"`
	UpdatedAt       httpx.Time `json:"updated_at"`
}

func (n noteRow) JSON() noteJSON {
	return noteJSON{
		ID: n.ID, UserID: n.UserID, LessonID: n.LessonID, Content: n.Content,
		PositionSeconds: n.PositionSeconds,
		CreatedAt:       httpx.NewTime(n.CreatedAt), UpdatedAt: httpx.NewTime(n.UpdatedAt),
	}
}

// ---------------------------------------------------------------------------
// lesson_attachments

type attachmentRow struct {
	ID           int64     `gorm:"column:id"`
	LessonID     int64     `gorm:"column:lesson_id"`
	OriginalName string    `gorm:"column:original_name"`
	FilePath     string    `gorm:"column:file_path"`
	SizeBytes    *int64    `gorm:"column:size_bytes"`
	MimeType     *string   `gorm:"column:mime_type"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
}

type attachmentJSON struct {
	ID           int64      `json:"id"`
	LessonID     int64      `json:"lesson_id"`
	OriginalName string     `json:"original_name"`
	FilePath     string     `json:"file_path"`
	FileSize     *string    `json:"file_size"`
	MimeType     *string    `json:"mime_type"`
	CreatedAt    httpx.Time `json:"created_at"`
	UpdatedAt    httpx.Time `json:"updated_at"`
	URL          string     `json:"url"`
}

// formatFileSize renders bytes as the legacy file_size string:
// MediaService::upload's round(bytes/1024/1024, 2) . 'MB' (PHP float to string).
func formatFileSize(b *int64) *string {
	if b == nil {
		return nil
	}
	mb := math.Round(float64(*b)/1024/1024*100) / 100
	s := strconv.FormatFloat(mb, 'f', -1, 64) + "MB"
	return &s
}

func (m *Module) attachmentJSON(a attachmentRow) attachmentJSON {
	return attachmentJSON{
		ID: a.ID, LessonID: a.LessonID, OriginalName: a.OriginalName, FilePath: a.FilePath,
		FileSize: formatFileSize(a.SizeBytes), MimeType: a.MimeType,
		CreatedAt: httpx.NewTime(a.CreatedAt), UpdatedAt: httpx.NewTime(a.UpdatedAt),
		URL: m.publicURL(a.FilePath),
	}
}

// publicURL is MediaService::getUrl.
func (m *Module) publicURL(p string) string {
	if u := m.media.Public(&p); u != nil {
		return *u
	}
	return ""
}

// ---------------------------------------------------------------------------
// lesson_assignments

type assignmentRow struct {
	ID                int64      `gorm:"column:id"`
	LessonID          int64      `gorm:"column:lesson_id"`
	Instructions      string     `gorm:"column:instructions"`
	MaxFileSizeKB     int        `gorm:"column:max_file_size_kb"`
	AllowedExtensions string     `gorm:"column:allowed_extensions"` // array_to_string(.., ',')
	AvailableFrom     *time.Time `gorm:"column:available_from"`
	SubmissionOpenAt  *time.Time `gorm:"column:submission_open_at"`
	DueAt             *time.Time `gorm:"column:due_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

const assignmentCols = `id, lesson_id, instructions, max_file_size_kb,
	array_to_string(allowed_extensions, ',') AS allowed_extensions,
	available_from, submission_open_at, due_at, created_at, updated_at`

type assignmentJSON struct {
	ID                int64      `json:"id"`
	LessonID          int64      `json:"lesson_id"`
	Instructions      string     `json:"instructions"`
	MaxFileSize       int        `json:"max_file_size"`
	AllowedExtensions string     `json:"allowed_extensions"`
	AvailableFrom     httpx.Time `json:"available_from"`
	SubmissionOpenAt  httpx.Time `json:"submission_open_at"`
	DueAt             httpx.Time `json:"due_at"`
	CreatedAt         httpx.Time `json:"created_at"`
	UpdatedAt         httpx.Time `json:"updated_at"`
}

func (a assignmentRow) JSON() assignmentJSON {
	return assignmentJSON{
		ID: a.ID, LessonID: a.LessonID, Instructions: a.Instructions,
		MaxFileSize: a.MaxFileSizeKB, AllowedExtensions: a.AllowedExtensions,
		AvailableFrom: tptr(a.AvailableFrom), SubmissionOpenAt: tptr(a.SubmissionOpenAt), DueAt: tptr(a.DueAt),
		CreatedAt: httpx.NewTime(a.CreatedAt), UpdatedAt: httpx.NewTime(a.UpdatedAt),
	}
}

// parseExtensions turns the legacy CSV into the text[] column value
// (lower-cased, trimmed, empty entries dropped).
func parseExtensions(csv string) []string {
	var out []string
	for _, p := range strings.Split(csv, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// assignment_submissions

type submissionRow struct {
	ID                 int64      `gorm:"column:id"`
	LessonAssignmentID int64      `gorm:"column:lesson_assignment_id"`
	UserID             int64      `gorm:"column:user_id"`
	FilePath           string     `gorm:"column:file_path"`
	StudentNote        *string    `gorm:"column:student_note"`
	Grade              *string    `gorm:"column:grade"` // numeric(5,2) as text: decimal:2 cast
	Feedback           *string    `gorm:"column:feedback"`
	SubmittedAt        *time.Time `gorm:"column:submitted_at"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
	// optional user:id,name,email
	UName  *string `gorm:"column:u_name"`
	UEmail *string `gorm:"column:u_email"`
}

const submissionCols = `s.id, s.lesson_assignment_id, s.user_id, s.file_path, s.student_note,
	s.grade::text AS grade, s.feedback, s.submitted_at, s.created_at, s.updated_at`

type userBrief struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type submissionJSON struct {
	ID                 int64      `json:"id"`
	LessonAssignmentID int64      `json:"lesson_assignment_id"`
	UserID             int64      `json:"user_id"`
	FileURL            string     `json:"file_url"`
	StudentNote        *string    `json:"student_note"`
	Grade              *string    `json:"grade"`
	Feedback           *string    `json:"feedback"`
	SubmittedAt        httpx.Time `json:"submitted_at"`
	CreatedAt          httpx.Time `json:"created_at"`
	UpdatedAt          httpx.Time `json:"updated_at"`
}

// submissionUserJSON adds the `user:id,name,email` relation.
type submissionUserJSON struct {
	submissionJSON
	User *userBrief `json:"user"`
}

// submissionJSON rebuilds the legacy file_url (full public URL) from file_path.
func (m *Module) submissionJSON(s submissionRow) submissionJSON {
	return submissionJSON{
		ID: s.ID, LessonAssignmentID: s.LessonAssignmentID, UserID: s.UserID,
		FileURL: m.publicURL(s.FilePath), StudentNote: s.StudentNote, Grade: s.Grade, Feedback: s.Feedback,
		SubmittedAt: tptr(s.SubmittedAt),
		CreatedAt:   httpx.NewTime(s.CreatedAt), UpdatedAt: httpx.NewTime(s.UpdatedAt),
	}
}

// submissionUserJSON is submissionJSON plus the user (null when the row is gone).
func (m *Module) submissionUserJSON(s submissionRow) submissionUserJSON {
	out := submissionUserJSON{submissionJSON: m.submissionJSON(s)}
	if s.UName != nil {
		out.User = &userBrief{ID: s.UserID, Name: *s.UName, Email: deref(s.UEmail)}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
