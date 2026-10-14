package interaction

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

const qasPerPage = 20

// listQas is CourseQaController@index: newest first, 20 per page, optional
// ?lesson_id filter (present but empty means "no lesson", like where(col, null)).
func (m *Module) listQas(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	uid := auth.UserID(c)
	p := httpx.ParsePage(c, qasPerPage, qasPerPage)
	p.PerPage = qasPerPage

	where, args := `course_id = ?`, []any{course.ID}
	if vals, ok := c.GetQueryArray("lesson_id"); ok {
		v := ""
		if len(vals) > 0 {
			v = vals[len(vals)-1]
		}
		in := &input{vals: map[string]any{}}
		in.set("lesson_id", v)
		switch {
		case in.vals["lesson_id"] == nil:
			where += ` AND lesson_id IS NULL`
		case isInteger(in.vals["lesson_id"]):
			id, _ := in.int("lesson_id")
			where += ` AND lesson_id = ?`
			args = append(args, id)
		default:
			where += ` AND false` // MySQL compared a non-numeric string as 0: no match
		}
	}

	db := m.db.WithContext(ctx)
	var total int64
	if err := db.Raw(`SELECT count(*) FROM course_qas WHERE `+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	var qas []qaRow
	if err := db.Raw(`SELECT `+qaCols+` FROM course_qas WHERE `+where+`
ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, append(args, p.Limit(), p.Offset())...).Scan(&qas).Error; err != nil {
		httpx.Fail(c, err)
		return
	}

	qaIDs := make([]int64, 0, len(qas))
	userIDs := make([]int64, 0, len(qas))
	for _, q := range qas {
		qaIDs = append(qaIDs, q.ID)
		userIDs = append(userIDs, q.UserID)
	}
	replies, err := m.loadReplies(ctx, qaIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	replyIDs := make([]int64, 0)
	for _, rs := range replies {
		for _, r := range rs {
			replyIDs = append(replyIDs, r.ID)
			userIDs = append(userIDs, r.UserID)
		}
	}
	qaReacts, err := m.loadReactions(ctx, "course_qa_id", qaIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	replyReacts, err := m.loadReactions(ctx, "course_qa_reply_id", replyIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	users, err := m.loadUsers(ctx, userIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	items := make([]qaListItem, 0, len(qas))
	for _, q := range qas {
		item := qaListItem{
			qaRow:         q,
			User:          users[q.UserID],
			Reactions:     reactionsJSON(qaReacts[q.ID]),
			Replies:       make([]replyItem, 0, len(replies[q.ID])),
			reactionState: computeReactionState(qaReacts[q.ID], uid),
		}
		for _, r := range replies[q.ID] {
			item.Replies = append(item.Replies, replyItem{
				replyRow:      r,
				User:          users[r.UserID],
				Reactions:     reactionsJSON(replyReacts[r.ID]),
				reactionState: computeReactionState(replyReacts[r.ID], uid),
			})
		}
		items = append(items, item)
	}
	httpx.Paginate(c, items, total, p)
}

// loadReplies returns qa id -> replies ordered by created_at.
func (m *Module) loadReplies(ctx context.Context, qaIDs []int64) (map[int64][]replyRow, error) {
	out := map[int64][]replyRow{}
	if len(qaIDs) == 0 {
		return out, nil
	}
	var rows []replyRow
	if err := m.db.WithContext(ctx).Raw(`SELECT `+replyCols+` FROM course_qa_replies WHERE course_qa_id IN ?
ORDER BY created_at, id`, qaIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.CourseQaID] = append(out[r.CourseQaID], r)
	}
	return out, nil
}

// loadReactions returns target id -> reactions (id order) for col
// course_qa_id or course_qa_reply_id.
func (m *Module) loadReactions(ctx context.Context, col string, ids []int64) (map[int64][]reactionRow, error) {
	out := map[int64][]reactionRow{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []reactionRow
	if err := m.db.WithContext(ctx).Raw(`SELECT id, user_id, course_qa_id, course_qa_reply_id, kind, created_at, updated_at
FROM qa_reactions WHERE `+col+` IN ? ORDER BY id`, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		key := r.QaID
		if col == "course_qa_reply_id" {
			key = r.ReplyID
		}
		if key != nil {
			out[*key] = append(out[*key], r)
		}
	}
	return out, nil
}

// canParticipate is the store/storeReply check: enrolled, admin or course owner.
func (m *Module) canParticipate(c *gin.Context, course learning.CourseRef) (bool, bool) {
	who, ok := m.caller(c)
	if !ok {
		return false, false
	}
	enrolled, err := learning.IsEnrolled(c.Request.Context(), m.db, who.UserID, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return false, false
	}
	return enrolled || who.IsOwner(course), true
}

// storeQa is CourseQaController@store.
func (m *Module) storeQa(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	allowed, ok := m.canParticipate(c, course)
	if !ok {
		return
	}
	if !allowed {
		httpx.Abort(c, http.StatusForbidden, "Bạn cần đăng ký khóa học để đặt câu hỏi.")
		return
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, m.exists(ctx),
		rules("lesson_id", "nullable|exists:lessons,id"),
		rules("subject", "required|string|max:255"),
		rules("content", "required|string"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	var lessonID *int64
	if id, ok := in.int("lesson_id"); ok {
		lessonID = &id
	}
	uid := auth.UserID(c)
	var rows []qaRow
	if err := m.db.WithContext(ctx).Raw(`INSERT INTO course_qas (user_id, course_id, lesson_id, subject, content)
VALUES (?, ?, ?, ?, ?) RETURNING `+qaCols, uid, course.ID, lessonID, *in.str("subject"), *in.str("content")).Scan(&rows).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(rows) == 0 {
		httpx.Fail(c, errors.New("interaction: insert course_qas returned no row"))
		return
	}
	user, err := m.loadUser(ctx, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, qaWithUser{qaRow: rows[0], User: user})
}

// showQa is CourseQaController@show (user + replies.user, no reactions).
func (m *Module) showQa(c *gin.Context) {
	ctx := c.Request.Context()
	qa := qaOf(c)
	if qa.CourseID != courseOf(c).ID {
		notFound(c)
		return
	}
	replies, err := m.loadReplies(ctx, []int64{qa.ID})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	userIDs := []int64{qa.UserID}
	for _, r := range replies[qa.ID] {
		userIDs = append(userIDs, r.UserID)
	}
	users, err := m.loadUsers(ctx, userIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := qaShow{qaRow: qa, User: users[qa.UserID], Replies: make([]replyUser, 0, len(replies[qa.ID]))}
	for _, r := range replies[qa.ID] {
		out.Replies = append(out.Replies, replyUser{replyRow: r, User: users[r.UserID]})
	}
	httpx.OK(c, out)
}

// storeReply is CourseQaController@storeReply.
func (m *Module) storeReply(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	qa := qaOf(c)
	if qa.CourseID != course.ID {
		notFound(c)
		return
	}
	allowed, ok := m.canParticipate(c, course)
	if !ok {
		return
	}
	if !allowed {
		forbidden(c)
		return
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil, rules("content", "required|string")); err != nil {
		httpx.Fail(c, err)
		return
	}
	uid := auth.UserID(c)
	var rows []replyRow
	if err := m.db.WithContext(ctx).Raw(`INSERT INTO course_qa_replies (course_qa_id, user_id, content)
VALUES (?, ?, ?) RETURNING `+replyCols, qa.ID, uid, *in.str("content")).Scan(&rows).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(rows) == 0 {
		httpx.Fail(c, errors.New("interaction: insert course_qa_replies returned no row"))
		return
	}
	user, err := m.loadUser(ctx, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, replyCreated{replyRow: rows[0], User: user})
}

// react is CourseQaController@react: toggle like/dislike on a question or a
// reply of this course and return the fresh reaction state.
func (m *Module) react(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	uid := auth.UserID(c)
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil,
		rules("reactable_type", "required|string|in:qa,reply"),
		rules("reactable_id", "required|integer"),
		rules("kind", "required|string|in:like,dislike"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	targetID, _ := in.int("reactable_id")
	kind := *in.str("kind")

	col := "course_qa_id"
	var findSQL string
	if *in.str("reactable_type") == "qa" {
		findSQL = `SELECT count(*) FROM course_qas WHERE id = ? AND course_id = ?`
	} else {
		col = "course_qa_reply_id"
		findSQL = `SELECT count(*) FROM course_qa_replies r JOIN course_qas q ON q.id = r.course_qa_id
WHERE r.id = ? AND q.course_id = ?`
	}

	var state reactionState
	found := true
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Raw(findSQL, targetID, course.ID).Scan(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			found = false
			return nil
		}
		var existing []reactionRow
		if err := tx.Raw(`SELECT id, user_id, course_qa_id, course_qa_reply_id, kind, created_at, updated_at
FROM qa_reactions WHERE user_id = ? AND `+col+` = ? FOR UPDATE`, uid, targetID).Scan(&existing).Error; err != nil {
			return err
		}
		var err error
		switch {
		case len(existing) == 0:
			err = tx.Exec(`INSERT INTO qa_reactions (user_id, `+col+`, kind) VALUES (?, ?, ?)
ON CONFLICT (`+col+`, user_id) WHERE `+col+` IS NOT NULL DO UPDATE SET kind = EXCLUDED.kind`, uid, targetID, kind).Error
		case existing[0].Kind == kind:
			err = tx.Exec(`DELETE FROM qa_reactions WHERE id = ?`, existing[0].ID).Error // toggle off
		default:
			err = tx.Exec(`UPDATE qa_reactions SET kind = ? WHERE id = ?`, kind, existing[0].ID).Error // flip
		}
		if err != nil {
			return err
		}
		var all []reactionRow
		if err := tx.Raw(`SELECT id, user_id, course_qa_id, course_qa_reply_id, kind, created_at, updated_at
FROM qa_reactions WHERE `+col+` = ? ORDER BY id`, targetID).Scan(&all).Error; err != nil {
			return err
		}
		state = computeReactionState(all, uid)
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !found {
		notFound(c)
		return
	}
	httpx.OK(c, state)
}

// exists answers exists:table,column for the tables used by the validators.
func (m *Module) exists(ctx context.Context) existsFunc {
	return func(table, column string, value any) (bool, error) {
		if table != "lessons" || column != "id" || !isInteger(value) {
			return false, nil
		}
		in := &input{vals: map[string]any{"v": value}}
		id, ok := in.int("v")
		if !ok {
			return false, nil
		}
		var n int64
		if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM lessons WHERE id = ?`, id).Scan(&n).Error; err != nil {
			return false, err
		}
		return n > 0, nil
	}
}
