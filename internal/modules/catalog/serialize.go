package catalog

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

// JSON shapes mirror the legacy Eloquent toArray() output: attributes in the
// legacy column order with legacy names (user_id, thumbnail, order, duration,
// video_url, video_size...), then withCount/withAvg attributes, then relations
// in eager-load order. Values are rebuilt from the new columns (docs/db/03_learning.md §3).

// ---------------------------------------------------------------------------
// courses

// courseCols selects a courseRow from "courses c".
const courseCols = `c.id, c.instructor_id, c.category_id, c.program_id, c.major_id, c.title, c.slug, c.description,
 c.learning_outcomes::text AS learning_outcomes, c.benefits::text AS benefits, c.requirements::text AS requirements,
 c.level, c.trailer_url, c.price::text AS price, c.course_mode, c.is_credit_bearing, c.credit_value, c.status,
 c.is_featured, c.reject_reason, c.thumbnail_path, c.published_at, c.created_at, c.updated_at, c.certificate_template_id`

type courseRow struct {
	ID                    int64      `gorm:"column:id"`
	InstructorID          int64      `gorm:"column:instructor_id"`
	CategoryID            *int64     `gorm:"column:category_id"`
	ProgramID             *int64     `gorm:"column:program_id"`
	MajorID               *int64     `gorm:"column:major_id"`
	Title                 string     `gorm:"column:title"`
	Slug                  string     `gorm:"column:slug"`
	Description           *string    `gorm:"column:description"`
	LearningOutcomes      string     `gorm:"column:learning_outcomes"`
	Benefits              string     `gorm:"column:benefits"`
	Requirements          string     `gorm:"column:requirements"`
	Level                 *string    `gorm:"column:level"`
	TrailerURL            *string    `gorm:"column:trailer_url"`
	Price                 string     `gorm:"column:price"`
	CourseMode            string     `gorm:"column:course_mode"`
	IsCreditBearing       bool       `gorm:"column:is_credit_bearing"`
	CreditValue           *int64     `gorm:"column:credit_value"`
	Status                string     `gorm:"column:status"`
	IsFeatured            bool       `gorm:"column:is_featured"`
	RejectReason          *string    `gorm:"column:reject_reason"`
	ThumbnailPath         *string    `gorm:"column:thumbnail_path"`
	PublishedAt           httpx.Time `gorm:"column:published_at"`
	CreatedAt             httpx.Time `gorm:"column:created_at"`
	UpdatedAt             httpx.Time `gorm:"column:updated_at"`
	CertificateTemplateID *int64     `gorm:"column:certificate_template_id"`
}

func (r courseRow) ref() learning.CourseRef {
	return learning.CourseRef{ID: r.ID, InstructorID: r.InstructorID, Status: r.Status, Price: r.Price}
}

// courseAttrs is Course::toArray() without relations.
type courseAttrs struct {
	ID                    int64           `json:"id"`
	UserID                int64           `json:"user_id"`
	CategoryID            *int64          `json:"category_id"`
	ProgramTypeID         *int64          `json:"program_type_id"` // dropped column, always null
	ProgramID             *int64          `json:"program_id"`
	MajorID               *int64          `json:"major_id"`
	CurriculumID          *int64          `json:"curriculum_id"` // dropped column, always null
	Title                 string          `json:"title"`
	Slug                  string          `json:"slug"`
	Description           *string         `json:"description"`
	LearningOutcomes      json.RawMessage `json:"learning_outcomes"`
	Benefits              json.RawMessage `json:"benefits"`
	Requirements          json.RawMessage `json:"requirements"`
	Level                 *string         `json:"level"`
	TrailerURL            *string         `json:"trailer_url"`
	Price                 int64           `json:"price"` // 'price' => 'integer' cast
	CourseMode            string          `json:"course_mode"`
	IsCreditBearing       bool            `json:"is_credit_bearing"`
	CreditValue           *int64          `json:"credit_value"`
	Status                string          `json:"status"`
	IsFeatured            bool            `json:"is_featured"`
	RejectReason          *string         `json:"reject_reason"`
	Thumbnail             *string         `json:"thumbnail"`
	PublishedAt           httpx.Time      `json:"published_at"`
	CreatedAt             httpx.Time      `json:"created_at"`
	UpdatedAt             httpx.Time      `json:"updated_at"`
	CertificateTemplateID *int64          `json:"certificate_template_id"`
}

func (m *Module) courseAttrs(r courseRow) courseAttrs {
	return courseAttrs{
		ID: r.ID, UserID: r.InstructorID, CategoryID: r.CategoryID, ProgramID: r.ProgramID, MajorID: r.MajorID,
		Title: r.Title, Slug: r.Slug, Description: r.Description,
		LearningOutcomes: jsonArray(r.LearningOutcomes), Benefits: jsonArray(r.Benefits), Requirements: jsonArray(r.Requirements),
		Level: r.Level, TrailerURL: r.TrailerURL, Price: intPrice(r.Price), CourseMode: r.CourseMode,
		IsCreditBearing: r.IsCreditBearing, CreditValue: r.CreditValue, Status: r.Status, IsFeatured: r.IsFeatured,
		RejectReason: r.RejectReason, Thumbnail: m.media.Public(r.ThumbnailPath),
		PublishedAt: r.PublishedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		CertificateTemplateID: r.CertificateTemplateID,
	}
}

// jsonArray returns a stored jsonb value ([] when empty).
func jsonArray(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("[]")
	}
	return json.RawMessage(s)
}

// intPrice is PHP (int) of a numeric text ("199000.00" -> 199000).
func intPrice(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// userBrief is instructor:id,name,avatar / user:id,name,avatar.
type userBrief struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Avatar *string `json:"avatar"`
}

// categoryBrief is category:id,name,slug.
type categoryBrief struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// courseListItem is an element of GET /courses.
type courseListItem struct {
	courseAttrs
	LessonsCount     int64          `json:"lessons_count"`
	EnrollmentsCount int64          `json:"enrollments_count"`
	ReviewsCount     int64          `json:"reviews_count"`
	ReviewsAvgRating *float64       `json:"reviews_avg_rating"`
	Instructor       *userBrief     `json:"instructor"`
	Category         *categoryBrief `json:"category"`
}

// courseWithCounts is GET /my-courses items and the update response
// (withCount lessons, enrollments + instructor, category).
type courseWithCounts struct {
	courseAttrs
	LessonsCount     int64          `json:"lessons_count"`
	EnrollmentsCount int64          `json:"enrollments_count"`
	Instructor       *userBrief     `json:"instructor"`
	Category         *categoryBrief `json:"category"`
}

// courseCreated is the store response (instructor + counts, no category).
type courseCreated struct {
	courseAttrs
	LessonsCount     int64      `json:"lessons_count"`
	EnrollmentsCount int64      `json:"enrollments_count"`
	Instructor       *userBrief `json:"instructor"`
}

// courseDetail is GET /courses/{course}.
type courseDetail struct {
	courseAttrs
	EnrollmentsCount int64                `json:"enrollments_count"`
	ReviewsCount     int64                `json:"reviews_count"`
	LessonsCount     int64                `json:"lessons_count"`
	Instructor       *userBrief           `json:"instructor"`
	Category         *categoryBrief       `json:"category"`
	Lessons          []lessonJSON         `json:"lessons"`
	Sections         []sectionOutline     `json:"sections"`
	IsEnrolled       bool                 `json:"is_enrolled"`
	HasReviewed      bool                 `json:"has_reviewed"`
	AvgRating        float64              `json:"avg_rating"`
	LatestReviews    []reviewJSON         `json:"latest_reviews"`
	PathSuggestions  []pathSuggestionJSON `json:"path_suggestions"`
}

type reviewJSON struct {
	ID        int64      `json:"id"`
	UserID    int64      `json:"user_id"`
	CourseID  int64      `json:"course_id"`
	Rating    int64      `json:"rating"`
	Comment   *string    `json:"comment"`
	CreatedAt httpx.Time `json:"created_at"`
	UpdatedAt httpx.Time `json:"updated_at"`
	User      *userBrief `json:"user"`
}

type pathSuggestionJSON struct {
	ID                  int64             `json:"id"`
	Title               string            `json:"title"`
	Slug                string            `json:"slug"`
	PathPrice           int64             `json:"path_price"`
	TotalCount          int               `json:"total_count"`
	OwnedCount          int               `json:"owned_count"`
	RemainingCount      int               `json:"remaining_count"`
	RemainingTotalPrice int64             `json:"remaining_total_price"`
	RemainingCourses    []pathCourseBrief `json:"remaining_courses"`
}

type pathCourseBrief struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Slug      string  `json:"slug"`
	Thumbnail *string `json:"thumbnail"`
	Price     int64   `json:"price"`
}

// ---------------------------------------------------------------------------
// categories

type categoryRow struct {
	ID        int64      `gorm:"column:id"`
	ParentID  *int64     `gorm:"column:parent_id"`
	Name      string     `gorm:"column:name"`
	Slug      string     `gorm:"column:slug"`
	Icon      *string    `gorm:"column:icon"`
	SortOrder int64      `gorm:"column:sort_order"`
	CreatedAt httpx.Time `gorm:"column:created_at"`
	UpdatedAt httpx.Time `gorm:"column:updated_at"`
}

// categoryAttrs is Category::toArray() without relations (icon is a plain value).
type categoryAttrs struct {
	ID        int64      `json:"id"`
	ParentID  *int64     `json:"parent_id"`
	Name      string     `json:"name"`
	Slug      string     `json:"slug"`
	Icon      *string    `json:"icon"`
	SortOrder int64      `json:"sort_order"`
	CreatedAt httpx.Time `json:"created_at"`
	UpdatedAt httpx.Time `json:"updated_at"`
}

func (r categoryRow) attrs() categoryAttrs { return categoryAttrs(r) }

// categoryNode is a category with courses_count and (when loaded) children.
type categoryNode struct {
	categoryAttrs
	CoursesCount int64           `json:"courses_count"`
	Children     *[]categoryNode `json:"children,omitempty"`
}

// categoryParent is parent:id,name,slug,parent_id.
type categoryParent struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ParentID *int64 `json:"parent_id"`
}

// categoryDetail is GET /categories/{category}.
type categoryDetail struct {
	categoryAttrs
	CoursesCount int64           `json:"courses_count"`
	Parent       *categoryParent `json:"parent"`
	Children     []categoryNode  `json:"children"`
}

// programCategory is an element of GET /courses/categories.
type programCategory struct {
	categoryAttrs
	CoursesCount           int64           `json:"courses_count"`
	CurriculumCoursesCount int64           `json:"curriculum_courses_count"`
	ExtensionCoursesCount  int64           `json:"extension_courses_count"`
	Children               []categoryAttrs `json:"children"`
}

// ---------------------------------------------------------------------------
// sections & lessons

const sectionCols = `s.id, s.course_id, s.title, s.description, s.position, s.created_at, s.updated_at`

type sectionRow struct {
	ID          int64      `gorm:"column:id"`
	CourseID    int64      `gorm:"column:course_id"`
	Title       string     `gorm:"column:title"`
	Description *string    `gorm:"column:description"`
	Position    int64      `gorm:"column:position"`
	CreatedAt   httpx.Time `gorm:"column:created_at"`
	UpdatedAt   httpx.Time `gorm:"column:updated_at"`
}

type sectionJSON struct {
	ID          int64      `json:"id"`
	CourseID    int64      `json:"course_id"`
	Title       string     `json:"title"`
	Description *string    `json:"description"`
	Position    int64      `json:"position"`
	CreatedAt   httpx.Time `json:"created_at"`
	UpdatedAt   httpx.Time `json:"updated_at"`
}

func (r sectionRow) json() sectionJSON { return sectionJSON(r) }

// sectionOutline is a section with the public lesson outline.
type sectionOutline struct {
	sectionJSON
	Lessons []lessonOutline `json:"lessons"`
}

// sectionWithLessons is SectionController::show (full lessons).
type sectionWithLessons struct {
	sectionJSON
	Lessons []lessonJSON `json:"lessons"`
}

// sectionBuilder is the owner view of SectionController::index.
type sectionBuilder struct {
	sectionJSON
	Lessons []lessonBuilder `json:"lessons"`
}

const lessonCols = `l.id, l.course_id, l.section_id, l.title, l.type, l.description, l.video_path, l.duration_seconds,
 l.position, l.is_preview, l.created_at, l.updated_at, l.video_size_bytes, l.video_status`

type lessonRow struct {
	ID              int64      `gorm:"column:id"`
	CourseID        int64      `gorm:"column:course_id"`
	SectionID       *int64     `gorm:"column:section_id"`
	Title           string     `gorm:"column:title"`
	Type            string     `gorm:"column:type"`
	Description     *string    `gorm:"column:description"`
	VideoPath       *string    `gorm:"column:video_path"`
	DurationSeconds int64      `gorm:"column:duration_seconds"`
	Position        int64      `gorm:"column:position"`
	IsPreview       bool       `gorm:"column:is_preview"`
	CreatedAt       httpx.Time `gorm:"column:created_at"`
	UpdatedAt       httpx.Time `gorm:"column:updated_at"`
	VideoSizeBytes  *int64     `gorm:"column:video_size_bytes"`
	VideoStatus     string     `gorm:"column:video_status"`
}

// lessonJSON is Lesson::toArray() (optionally with attachments loaded).
type lessonJSON struct {
	ID          int64      `json:"id"`
	CourseID    int64      `json:"course_id"`
	SectionID   *int64     `json:"section_id"`
	Title       string     `json:"title"`
	Type        string     `json:"type"`
	Description *string    `json:"description"`
	VideoURL    *string    `json:"video_url"`
	Duration    int64      `json:"duration"`
	Order       int64      `json:"order"`
	IsPreview   bool       `json:"is_preview"`
	CreatedAt   httpx.Time `json:"created_at"`
	UpdatedAt   httpx.Time `json:"updated_at"`
	VideoSize   *string    `json:"video_size"`
	VideoStatus string     `json:"video_status"`

	Attachments *[]attachmentJSON `json:"attachments,omitempty"`
}

func (m *Module) lessonJSON(r lessonRow) lessonJSON {
	return lessonJSON{
		ID: r.ID, CourseID: r.CourseID, SectionID: r.SectionID, Title: r.Title, Type: r.Type,
		Description: r.Description, VideoURL: m.media.Public(r.VideoPath), Duration: r.DurationSeconds,
		Order: r.Position, IsPreview: r.IsPreview, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		VideoSize: learning.FormatSizeMB(r.VideoSizeBytes), VideoStatus: r.VideoStatus,
	}
}

// lessonOutline is the column subset selected for public outlines
// ('id','section_id','course_id','title','type','duration','is_preview','order').
type lessonOutline struct {
	ID        int64  `json:"id"`
	SectionID *int64 `json:"section_id"`
	CourseID  int64  `json:"course_id"`
	Title     string `json:"title"`
	Type      string `json:"type"`
	Duration  int64  `json:"duration"`
	IsPreview bool   `json:"is_preview"`
	Order     int64  `json:"order"`
}

func lessonOutlineOf(r lessonRow) lessonOutline {
	return lessonOutline{ID: r.ID, SectionID: r.SectionID, CourseID: r.CourseID, Title: r.Title, Type: r.Type,
		Duration: r.DurationSeconds, IsPreview: r.IsPreview, Order: r.Position}
}

// lessonLocked is LessonController::index for visitors (no video_url).
type lessonLocked struct {
	ID          int64   `json:"id"`
	SectionID   *int64  `json:"section_id"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Order       int64   `json:"order"`
	Duration    int64   `json:"duration"`
	IsPreview   bool    `json:"is_preview"`
	VideoStatus string  `json:"video_status"`
	VideoSize   *string `json:"video_size"`
	Type        string  `json:"type"`
	Locked      bool    `json:"locked"`
}

func lessonLockedOf(r lessonRow) lessonLocked {
	return lessonLocked{ID: r.ID, SectionID: r.SectionID, Title: r.Title, Description: r.Description,
		Order: r.Position, Duration: r.DurationSeconds, IsPreview: r.IsPreview, VideoStatus: r.VideoStatus,
		VideoSize: learning.FormatSizeMB(r.VideoSizeBytes), Type: r.Type, Locked: !r.IsPreview}
}

// lessonDetail is LessonController::show (all content relations).
type lessonDetail struct {
	lessonJSON
	VirtualClass   *virtualClassJSON   `json:"virtual_class"`
	ScormPackage   *scormPackageJSON   `json:"scorm_package"`
	OfflineSession *offlineSessionJSON `json:"offline_session"`
	Assignment     *assignmentJSON     `json:"assignment"`
	Attachments    []attachmentJSON    `json:"attachments"`
	Quiz           *quizJSON           `json:"quiz"`
}

// lessonBuilder is a lesson of the owner section tree
// (with attachments, virtualClass, scormPackage, assignment).
type lessonBuilder struct {
	lessonJSON
	Attachments  []attachmentJSON  `json:"attachments"`
	VirtualClass *virtualClassJSON `json:"virtual_class"`
	ScormPackage *scormPackageJSON `json:"scorm_package"`
	Assignment   *assignmentJSON   `json:"assignment"`
}

// ---------------------------------------------------------------------------
// lesson content relations (owned by other modules; read here for embedding)

type attachmentRow struct {
	ID           int64      `gorm:"column:id"`
	LessonID     int64      `gorm:"column:lesson_id"`
	OriginalName string     `gorm:"column:original_name"`
	FilePath     string     `gorm:"column:file_path"`
	SizeBytes    *int64     `gorm:"column:size_bytes"`
	MimeType     *string    `gorm:"column:mime_type"`
	CreatedAt    httpx.Time `gorm:"column:created_at"`
	UpdatedAt    httpx.Time `gorm:"column:updated_at"`
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
	URL          *string    `json:"url,omitempty"` // only LessonController::show adds it
}

func attachmentOf(r attachmentRow) attachmentJSON {
	return attachmentJSON{ID: r.ID, LessonID: r.LessonID, OriginalName: r.OriginalName, FilePath: r.FilePath,
		FileSize: learning.FormatSizeMB(r.SizeBytes), MimeType: r.MimeType, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type virtualClassJSON struct {
	ID              int64      `json:"id" gorm:"column:id"`
	LessonID        int64      `json:"lesson_id" gorm:"column:lesson_id"`
	Provider        string     `json:"provider" gorm:"column:provider"`
	MeetingID       *string    `json:"meeting_id" gorm:"column:meeting_id"`
	MeetingPassword *string    `json:"meeting_password" gorm:"column:meeting_password"`
	JoinURL         *string    `json:"join_url" gorm:"column:join_url"`
	StartURL        *string    `json:"start_url" gorm:"column:start_url"`
	StartAt         httpx.Time `json:"start_at" gorm:"column:start_at"`
	Duration        int64      `json:"duration" gorm:"column:duration"`
	CreatedAt       httpx.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt       httpx.Time `json:"updated_at" gorm:"column:updated_at"`
}

const virtualClassCols = `id, lesson_id, provider, meeting_id, meeting_password, join_url, start_url, start_at,
 duration_minutes AS duration, created_at, updated_at`

type scormPackageJSON struct {
	ID         int64      `json:"id" gorm:"column:id"`
	LessonID   int64      `json:"lesson_id" gorm:"column:lesson_id"`
	UUID       string     `json:"uuid" gorm:"column:uuid"`
	Version    string     `json:"version" gorm:"column:version"`
	EntryURL   *string    `json:"entry_url" gorm:"column:entry_url"`
	Identifier *string    `json:"identifier" gorm:"column:identifier"`
	Title      *string    `json:"title" gorm:"column:title"`
	CreatedAt  httpx.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  httpx.Time `json:"updated_at" gorm:"column:updated_at"`
}

const scormPackageCols = `id, lesson_id, uuid::text AS uuid, version, entry_path AS entry_url, identifier, title, created_at, updated_at`

type offlineSessionJSON struct {
	ID                    int64      `json:"id" gorm:"column:id"`
	LessonID              *int64     `json:"lesson_id" gorm:"column:lesson_id"`
	Location              string     `json:"location" gorm:"column:location"`
	Room                  *string    `json:"room" gorm:"column:room"`
	QRToken               *string    `json:"qr_token" gorm:"column:qr_token"`
	QRExpiresAt           httpx.Time `json:"qr_expires_at" gorm:"column:qr_expires_at"`
	Latitude              *float64   `json:"latitude" gorm:"column:latitude"`
	Longitude             *float64   `json:"longitude" gorm:"column:longitude"`
	CheckInRadiusMeters   int64      `json:"check_in_radius_meters" gorm:"column:check_in_radius_meters"`
	IsActive              bool       `json:"is_active" gorm:"column:is_active"`
	QREnabled             bool       `json:"qr_enabled" gorm:"column:qr_enabled"`
	QRMode                string     `json:"qr_mode" gorm:"column:qr_mode"`
	QRRotateSeconds       int64      `json:"qr_rotate_seconds" gorm:"column:qr_rotate_seconds"`
	StartAt               httpx.Time `json:"start_at" gorm:"column:start_at"`
	Duration              *int64     `json:"duration" gorm:"column:duration"`
	MaxParticipants       *int64     `json:"max_participants" gorm:"column:max_participants"`
	CreatedAt             httpx.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt             httpx.Time `json:"updated_at" gorm:"column:updated_at"`
	ClassSectionID        *int64     `json:"class_section_id" gorm:"column:class_section_id"`
	CourseID              *int64     `json:"course_id" gorm:"column:course_id"`
	AdministrativeClassID *int64     `json:"administrative_class_id" gorm:"column:administrative_class_id"`
	Title                 *string    `json:"title" gorm:"column:title"`
}

const offlineSessionCols = `id, lesson_id, location, room, qr_token, qr_expires_at, latitude::float8 AS latitude,
 longitude::float8 AS longitude, check_in_radius_meters, is_active, qr_enabled, qr_mode, qr_rotate_seconds, start_at,
 duration_minutes AS duration, max_participants, created_at, updated_at, class_section_id, course_id,
 administrative_class_id, title`

type assignmentJSON struct {
	ID                int64      `json:"id" gorm:"column:id"`
	LessonID          int64      `json:"lesson_id" gorm:"column:lesson_id"`
	Instructions      string     `json:"instructions" gorm:"column:instructions"`
	MaxFileSize       int64      `json:"max_file_size" gorm:"column:max_file_size"`
	AllowedExtensions string     `json:"allowed_extensions" gorm:"column:allowed_extensions"`
	AvailableFrom     httpx.Time `json:"available_from" gorm:"column:available_from"`
	SubmissionOpenAt  httpx.Time `json:"submission_open_at" gorm:"column:submission_open_at"`
	DueAt             httpx.Time `json:"due_at" gorm:"column:due_at"`
	CreatedAt         httpx.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt         httpx.Time `json:"updated_at" gorm:"column:updated_at"`
}

const assignmentCols = `id, lesson_id, instructions, max_file_size_kb AS max_file_size,
 array_to_string(allowed_extensions, ',') AS allowed_extensions, available_from, submission_open_at, due_at,
 created_at, updated_at`

type quizRow struct {
	ID          int64      `gorm:"column:id"`
	LessonID    *int64     `gorm:"column:lesson_id"`
	CourseID    *int64     `gorm:"column:course_id"`
	ExamID      *int64     `gorm:"column:exam_id"`
	Scope       string     `gorm:"column:scope"`
	Title       string     `gorm:"column:title"`
	Description *string    `gorm:"column:description"`
	TimeLimit   *int64     `gorm:"column:time_limit"`
	PassScore   int64      `gorm:"column:pass_score"`
	Settings    *string    `gorm:"column:settings"`
	CreatedAt   httpx.Time `gorm:"column:created_at"`
	UpdatedAt   httpx.Time `gorm:"column:updated_at"`
}

const quizCols = `id, lesson_id, course_id, exam_id, scope, title, description, time_limit, pass_score,
 settings::text AS settings, created_at, updated_at`

type quizJSON struct {
	ID          int64           `json:"id"`
	LessonID    *int64          `json:"lesson_id"`
	CourseID    *int64          `json:"course_id"`
	ExamID      *int64          `json:"exam_id"`
	Scope       string          `json:"scope"`
	Title       string          `json:"title"`
	Description *string         `json:"description"`
	TimeLimit   *int64          `json:"time_limit"`
	PassScore   int64           `json:"pass_score"`
	Settings    json.RawMessage `json:"settings"`
	CreatedAt   httpx.Time      `json:"created_at"`
	UpdatedAt   httpx.Time      `json:"updated_at"`
}

func quizOf(r quizRow) *quizJSON {
	settings := json.RawMessage("null")
	if r.Settings != nil {
		settings = json.RawMessage(*r.Settings)
	}
	return &quizJSON{ID: r.ID, LessonID: r.LessonID, CourseID: r.CourseID, ExamID: r.ExamID, Scope: r.Scope,
		Title: r.Title, Description: r.Description, TimeLimit: r.TimeLimit, PassScore: r.PassScore,
		Settings: settings, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
