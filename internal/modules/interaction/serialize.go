package interaction

import (
	"context"
	"slices"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// userJSON is a User loaded with `user:id,name,avatar` (avatar through the
// PublicMediaUrl accessor).
type userJSON struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	Avatar *string `json:"avatar"`
}

// loadUsers returns id -> user JSON for the given ids.
func (m *Module) loadUsers(ctx context.Context, ids []int64) (map[int64]*userJSON, error) {
	out := map[int64]*userJSON{}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID     int64   `gorm:"column:id"`
		Name   string  `gorm:"column:name"`
		Avatar *string `gorm:"column:avatar_path"`
	}
	if err := m.db.WithContext(ctx).Raw(
		`SELECT id, name, avatar_path FROM users WHERE id IN ?`, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = &userJSON{ID: r.ID, Name: r.Name, Avatar: m.media.Public(r.Avatar)}
	}
	return out, nil
}

func (m *Module) loadUser(ctx context.Context, id int64) (*userJSON, error) {
	us, err := m.loadUsers(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	return us[id], nil
}

func uniqueIDs(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// ---------------------------------------------------------------------------
// Reviews

const reviewCols = `id, user_id, course_id, rating, comment, created_at, updated_at`

// reviewRow is a reviews row and also the Review model's toArray().
type reviewRow struct {
	ID        int64      `gorm:"column:id" json:"id"`
	UserID    int64      `gorm:"column:user_id" json:"user_id"`
	CourseID  int64      `gorm:"column:course_id" json:"course_id"`
	Rating    int        `gorm:"column:rating" json:"rating"`
	Comment   *string    `gorm:"column:comment" json:"comment"`
	CreatedAt httpx.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt httpx.Time `gorm:"column:updated_at" json:"updated_at"`
}

// reviewJSON is Review::with('user:id,name,avatar').
type reviewJSON struct {
	reviewRow
	User *userJSON `json:"user"`
}

// ---------------------------------------------------------------------------
// Q&A

const qaCols = `id, user_id, course_id, lesson_id, subject, content, created_at, updated_at`

// qaRow is a course_qas row / CourseQa attributes.
type qaRow struct {
	ID        int64      `gorm:"column:id" json:"id"`
	UserID    int64      `gorm:"column:user_id" json:"user_id"`
	CourseID  int64      `gorm:"column:course_id" json:"course_id"`
	LessonID  *int64     `gorm:"column:lesson_id" json:"lesson_id"`
	Subject   string     `gorm:"column:subject" json:"subject"`
	Content   string     `gorm:"column:content" json:"content"`
	CreatedAt httpx.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt httpx.Time `gorm:"column:updated_at" json:"updated_at"`
}

const replyCols = `id, course_qa_id, user_id, content, created_at, updated_at`

// replyRow is a course_qa_replies row / CourseQaReply attributes.
type replyRow struct {
	ID         int64      `gorm:"column:id" json:"id"`
	CourseQaID int64      `gorm:"column:course_qa_id" json:"course_qa_id"`
	UserID     int64      `gorm:"column:user_id" json:"user_id"`
	Content    string     `gorm:"column:content" json:"content"`
	CreatedAt  httpx.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt  httpx.Time `gorm:"column:updated_at" json:"updated_at"`
}

// Legacy morph class names of qa_reactions.reactable_type.
const (
	morphQa    = `App\Models\CourseQa`
	morphReply = `App\Models\CourseQaReply`
)

// reactionRow is a qa_reactions row.
type reactionRow struct {
	ID        int64      `gorm:"column:id"`
	UserID    int64      `gorm:"column:user_id"`
	QaID      *int64     `gorm:"column:course_qa_id"`
	ReplyID   *int64     `gorm:"column:course_qa_reply_id"`
	Kind      string     `gorm:"column:kind"`
	CreatedAt httpx.Time `gorm:"column:created_at"`
	UpdatedAt httpx.Time `gorm:"column:updated_at"`
}

// reactionJSON is the legacy QaReaction toArray() (polymorphic columns rebuilt).
type reactionJSON struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	ReactableType string     `json:"reactable_type"`
	ReactableID   int64      `json:"reactable_id"`
	Kind          string     `json:"kind"`
	CreatedAt     httpx.Time `json:"created_at"`
	UpdatedAt     httpx.Time `json:"updated_at"`
}

func (r reactionRow) toJSON() reactionJSON {
	out := reactionJSON{ID: r.ID, UserID: r.UserID, Kind: r.Kind, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.QaID != nil {
		out.ReactableType, out.ReactableID = morphQa, *r.QaID
	} else if r.ReplyID != nil {
		out.ReactableType, out.ReactableID = morphReply, *r.ReplyID
	}
	return out
}

// reactionState is HasQaReactions::appendReactionState.
type reactionState struct {
	LikeCount    int     `json:"like_count"`
	DislikeCount int     `json:"dislike_count"`
	MyReaction   *string `json:"my_reaction"`
}

// computeReactionState counts likes/dislikes and returns the kind of the
// first reaction of userID (0 = guest -> null).
func computeReactionState(rs []reactionRow, userID int64) reactionState {
	st := reactionState{}
	for _, r := range rs {
		switch r.Kind {
		case "like":
			st.LikeCount++
		case "dislike":
			st.DislikeCount++
		}
		if userID != 0 && st.MyReaction == nil && r.UserID == userID {
			k := r.Kind
			st.MyReaction = &k
		}
	}
	return st
}

func reactionsJSON(rs []reactionRow) []reactionJSON {
	out := make([]reactionJSON, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.toJSON())
	}
	return out
}

// qaListItem is one item of GET /qas (toArray + reaction state, replies with theirs).
type qaListItem struct {
	qaRow
	User      *userJSON      `json:"user"`
	Reactions []reactionJSON `json:"reactions"`
	Replies   []replyItem    `json:"replies"`
	reactionState
}

// replyItem is a reply with user, reactions and reaction state.
type replyItem struct {
	replyRow
	User      *userJSON      `json:"user"`
	Reactions []reactionJSON `json:"reactions"`
	reactionState
}

// qaShow is CourseQa loaded with user and replies.user.
type qaShow struct {
	qaRow
	User    *userJSON   `json:"user"`
	Replies []replyUser `json:"replies"`
}

// qaWithUser is a created question with its user.
type qaWithUser struct {
	qaRow
	User *userJSON `json:"user"`
}

// replyUser is a reply with its user.
type replyUser struct {
	replyRow
	User *userJSON `json:"user"`
}

// replyCreated is the storeReply response.
type replyCreated struct {
	replyRow
	User *userJSON `json:"user"`
	reactionState
}

// ---------------------------------------------------------------------------
// Virtual classes / SCORM packages

const virtualClassCols = `id, lesson_id, provider, meeting_id, meeting_password, join_url, start_url, start_at, duration_minutes, created_at, updated_at`

// virtualClassJSON is VirtualClass::toArray() (duration_minutes -> duration).
type virtualClassJSON struct {
	ID              int64      `gorm:"column:id" json:"id"`
	LessonID        int64      `gorm:"column:lesson_id" json:"lesson_id"`
	Provider        string     `gorm:"column:provider" json:"provider"`
	MeetingID       *string    `gorm:"column:meeting_id" json:"meeting_id"`
	MeetingPassword *string    `gorm:"column:meeting_password" json:"meeting_password"`
	JoinURL         string     `gorm:"column:join_url" json:"join_url"`
	StartURL        *string    `gorm:"column:start_url" json:"start_url"`
	StartAt         httpx.Time `gorm:"column:start_at" json:"start_at"`
	Duration        int        `gorm:"column:duration_minutes" json:"duration"`
	CreatedAt       httpx.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt       httpx.Time `gorm:"column:updated_at" json:"updated_at"`
}

const scormPackageCols = `id, lesson_id, uuid::text AS uuid, version, entry_path, identifier, title, created_at, updated_at`

// scormPackageRow is a scorm_packages row.
type scormPackageRow struct {
	ID         int64      `gorm:"column:id"`
	LessonID   int64      `gorm:"column:lesson_id"`
	UUID       string     `gorm:"column:uuid"`
	Version    string     `gorm:"column:version"`
	EntryPath  string     `gorm:"column:entry_path"`
	Identifier *string    `gorm:"column:identifier"`
	Title      *string    `gorm:"column:title"`
	CreatedAt  httpx.Time `gorm:"column:created_at"`
	UpdatedAt  httpx.Time `gorm:"column:updated_at"`
}

// scormPackageJSON is ScormPackage::toArray() with entry_url resolved.
type scormPackageJSON struct {
	ID         int64      `json:"id"`
	LessonID   int64      `json:"lesson_id"`
	UUID       string     `json:"uuid"`
	Version    string     `json:"version"`
	EntryURL   string     `json:"entry_url"`
	Identifier *string    `json:"identifier"`
	Title      *string    `json:"title"`
	CreatedAt  httpx.Time `json:"created_at"`
	UpdatedAt  httpx.Time `json:"updated_at"`
}

func (m *Module) scormPackageJSON(p scormPackageRow) scormPackageJSON {
	return scormPackageJSON{
		ID: p.ID, LessonID: p.LessonID, UUID: p.UUID, Version: p.Version,
		EntryURL:   resolveScormURL(p.EntryPath, m.bucket),
		Identifier: p.Identifier, Title: p.Title, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}
