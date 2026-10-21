package learning

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// notify stores an in-app notification; failures are non-fatal for the calling action.
func (m *Module) notify(uid int64, kind, title, message, link string) {
	_ = m.db.Exec("INSERT INTO notifications (user_id, type, title, message, link) VALUES (?, ?, ?, ?, NULLIF(?, ''))", uid, kind, title, message, link).Error
}

func (m *Module) notifications(c *gin.Context) {
	page, per := pageParams(c, 20, 50)
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', n.id, 'type', n.type, 'title', n.title, 'body', n.message, 'link', n.link,
		'read', n.read_at IS NOT NULL, 'created_at', n.created_at)
		FROM notifications n WHERE n.user_id = ? ORDER BY n.id DESC LIMIT ? OFFSET ?`, auth.UserID(c), per, (page-1)*per)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func (m *Module) unreadCount(c *gin.Context) {
	var n int64
	if err := m.db.Raw("SELECT count(*) FROM notifications WHERE user_id = ? AND read_at IS NULL", auth.UserID(c)).Scan(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"count": n})
}

func (m *Module) markRead(c *gin.Context) {
	id, ok := idParam(c, "id")
	if !ok {
		return
	}
	res := m.db.Exec("UPDATE notifications SET read_at = coalesce(read_at, now()) WHERE id = ? AND user_id = ?", id, auth.UserID(c))
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Marked as read."})
}

func (m *Module) markAllRead(c *gin.Context) {
	if err := m.db.Exec("UPDATE notifications SET read_at = now() WHERE user_id = ? AND read_at IS NULL", auth.UserID(c)).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Marked as read."})
}
