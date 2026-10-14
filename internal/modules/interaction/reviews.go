package interaction

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/learning"
)

const reviewsPerPage = 10

// listReviews is ReviewController@index (public, newest first, 10 per page).
func (m *Module) listReviews(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	p := httpx.ParsePage(c, reviewsPerPage, reviewsPerPage)
	p.PerPage = reviewsPerPage // paginate(10) ignores ?per_page

	var total int64
	if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM reviews WHERE course_id = ?`, course.ID).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	var rows []reviewRow
	if err := m.db.WithContext(ctx).Raw(`SELECT `+reviewCols+` FROM reviews WHERE course_id = ?
ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, course.ID, p.Limit(), p.Offset()).Scan(&rows).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	users, err := m.loadUsers(ctx, ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]reviewJSON, 0, len(rows))
	for _, r := range rows {
		items = append(items, reviewJSON{reviewRow: r, User: users[r.UserID]})
	}
	httpx.Paginate(c, items, total, p)
}

// storeReview is ReviewController@store: enrolled learners only, one review
// per course; the checks run before validation, as in Laravel.
func (m *Module) storeReview(c *gin.Context) {
	ctx := c.Request.Context()
	course := courseOf(c)
	uid := auth.UserID(c)

	enrolled, err := learning.IsEnrolled(ctx, m.db, uid, course.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !enrolled {
		httpx.Abort(c, http.StatusForbidden, "Bạn cần đăng ký khóa học trước khi đánh giá.")
		return
	}
	var n int64
	if err := m.db.WithContext(ctx).Raw(`SELECT count(*) FROM reviews WHERE user_id = ? AND course_id = ?`, uid, course.ID).Scan(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n > 0 {
		httpx.Abort(c, http.StatusUnprocessableEntity, "Bạn đã đánh giá khóa học này rồi.")
		return
	}

	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil,
		rules("rating", "required|integer|min:1|max:5"),
		rules("comment", "nullable|string|max:2000"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	rating, _ := in.int("rating")

	var rows []reviewRow
	err = m.db.WithContext(ctx).Raw(`INSERT INTO reviews (user_id, course_id, rating, comment) VALUES (?, ?, ?, ?)
ON CONFLICT (user_id, course_id) DO NOTHING RETURNING `+reviewCols, uid, course.ID, rating, in.str("comment")).Scan(&rows).Error
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(rows) == 0 { // lost a race with a concurrent request
		httpx.Abort(c, http.StatusUnprocessableEntity, "Bạn đã đánh giá khóa học này rồi.")
		return
	}
	user, err := m.loadUser(ctx, uid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, gin.H{
		"message": "Đánh giá thành công!",
		"review":  reviewJSON{reviewRow: rows[0], User: user},
	})
}

// updateReview is ReviewController@update (own review only). Like Laravel it
// does not check that the review belongs to {course}.
func (m *Module) updateReview(c *gin.Context) {
	ctx := c.Request.Context()
	review := reviewOf(c)
	if review.UserID != auth.UserID(c) {
		forbidden(c)
		return
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil,
		rules("rating", "sometimes|integer|min:1|max:5"),
		rules("comment", "nullable|string|max:2000"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}

	rating, comment := review.Rating, review.Comment
	if in.has("rating") {
		v, _ := in.int("rating")
		rating = int(v)
	}
	if in.has("comment") {
		comment = in.str("comment")
	}
	// Eloquent only saves (and touches updated_at) when an attribute changed.
	if rating != review.Rating || !sameStr(comment, review.Comment) {
		var rows []reviewRow
		if err := m.db.WithContext(ctx).Raw(`UPDATE reviews SET rating = ?, comment = ? WHERE id = ? RETURNING `+reviewCols,
			rating, comment, review.ID).Scan(&rows).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		if len(rows) == 0 {
			httpx.NotFound(c, modelNotFound("Review", c.Param("review")))
			return
		}
		review = rows[0]
	}
	user, err := m.loadUser(ctx, review.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"message": "Cập nhật đánh giá thành công!",
		"review":  reviewJSON{reviewRow: review, User: user},
	})
}

// destroyReview is ReviewController@destroy (owner or admin).
func (m *Module) destroyReview(c *gin.Context) {
	who, ok := m.caller(c)
	if !ok {
		return
	}
	review := reviewOf(c)
	if review.UserID != who.UserID && !who.IsAdmin() {
		forbidden(c)
		return
	}
	if err := m.db.WithContext(c.Request.Context()).Exec(`DELETE FROM reviews WHERE id = ?`, review.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Đã xóa đánh giá."})
}

func sameStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
