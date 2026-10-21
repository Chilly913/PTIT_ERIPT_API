package learning

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// Question banks: banks, groups, questions (+answers), JSON import and export.
// Contracts follow eript-web/src/features/qbank/types.ts.

const bankJSON = `jsonb_build_object('id', b.id, 'course_id', b.course_id, 'name', b.name, 'description', b.description,
  'question_count', (SELECT count(*) FROM questions q WHERE q.question_bank_id = b.id AND q.type <> 'passage'), -- a passage is a container, its sub-questions count
  'group_count', (SELECT count(*) FROM question_groups g WHERE g.question_bank_id = b.id),
  'difficulty_distribution', coalesce((SELECT jsonb_object_agg(d.difficulty::text, d.n) FROM
      (SELECT difficulty, count(*) n FROM questions q WHERE q.question_bank_id = b.id GROUP BY difficulty) d), '{}'::jsonb),
  'created_at', b.created_at)`

const questionCore = `jsonb_build_object('id', q.id, 'code', q.code, 'question_bank_id', q.question_bank_id, 'question_group_id', q.question_group_id, 'group', (SELECT g.name FROM question_groups g WHERE g.id = q.question_group_id),
  'type', q.type, 'content', q.content, 'difficulty', q.difficulty, 'default_score', q.default_score::float8,
  'explanation', q.explanation, 'feedback', q.feedback, 'metadata', q.metadata, 'used_in_quizzes', 0,
  'answers', coalesce((SELECT jsonb_agg(jsonb_build_object('id', a.id, 'content', a.content, 'sub_content', a.sub_content,
      'is_correct', a.is_correct, 'correct_position', a.correct_position) ORDER BY a.position) FROM question_answers a WHERE a.question_id = q.id), '[]'::jsonb))`

// childQuestionJSON is questionCore for the alias c (sub-questions of a passage).
var childQuestionJSON = strings.TrimSuffix(strings.ReplaceAll(questionCore, "q.", "c."), ")") + `, 'parent_question_id', c.parent_question_id)`

// questionJSON is questionCore plus the parent link and, for a passage, its sub-questions.
var questionJSON = strings.TrimSuffix(questionCore, ")") + `, 'parent_question_id', q.parent_question_id,
  'children', CASE WHEN q.type = 'passage' THEN coalesce((SELECT jsonb_agg(` + childQuestionJSON + ` ORDER BY c.id)
      FROM questions c WHERE c.parent_question_id = q.id), '[]'::jsonb) ELSE '[]'::jsonb END)`

type answerBody struct {
	Content         string  `json:"content"`
	SubContent      *string `json:"sub_content"`
	IsCorrect       bool    `json:"is_correct"`
	CorrectPosition *int    `json:"correct_position"`
}

type questionBody struct {
	Type            string   `json:"type"`
	Content         string   `json:"content"`
	Difficulty      int      `json:"difficulty"`
	DefaultScore    *float64 `json:"default_score"`
	Explanation     *string  `json:"explanation"`
	Feedback        *string  `json:"feedback"`
	QuestionGroupID *int64   `json:"question_group_id"`
	// Group is a group name used by import/export; the importer finds or creates the group in the bank.
	Group    *string        `json:"group"`
	Metadata map[string]any `json:"metadata"`
	Answers  []answerBody   `json:"answers"`
	// ParentQuestionID makes this a sub-question of a passage (create only). Children is the same relation seen from the
	// passage, used by JSON import/export.
	ParentQuestionID *int64         `json:"parent_question_id"`
	Children         []questionBody `json:"children"`
}

// check returns a message for the first problem found in the question, or "".
func (q *questionBody) check() string {
	if q.Type == "" {
		q.Type = "single_choice"
	}
	if q.Difficulty == 0 {
		q.Difficulty = 1
	}
	t, known := questionTypes[q.Type]
	switch {
	case !known:
		return "The selected type is invalid."
	case strings.TrimSpace(q.Content) == "":
		return "The content field is required."
	case q.Difficulty < 1 || q.Difficulty > 5:
		return "The difficulty must be between 1 and 5."
	case q.DefaultScore != nil && *q.DefaultScore < 0:
		return "The default score must be at least 0."
	}
	for _, a := range q.Answers {
		if strings.TrimSpace(a.Content) == "" {
			return "Every answer needs content."
		}
	}
	return t.check(q)
}

func newCode(bankID int64) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("Q%d-%s", bankID, strings.ToUpper(hex.EncodeToString(b)))
}

// bankOf resolves :ref and :bid and requires the caller to manage the course.
func (m *Module) bankOf(c *gin.Context) (courseID, bankID int64, ok bool) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return 0, 0, false
	}
	bid, ok := idParam(c, "bid")
	if !ok {
		return 0, 0, false
	}
	var n int64
	if err := m.db.Table("question_banks").Where("id = ? AND course_id = ?", bid, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return 0, 0, false
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return 0, 0, false
	}
	return cr.ID, bid, true
}

func (m *Module) listBanks(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 12, 100)
	conds, args := "b.course_id = ?", []any{cr.ID}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds += " AND b.name ILIKE ?"
		args = append(args, likeArg(s))
	}
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM question_banks b WHERE "+conds, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows("SELECT "+bankJSON+" FROM question_banks b WHERE "+conds+" ORDER BY b.created_at DESC, b.id DESC LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

type bankBody struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (b *bankBody) check() error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		return httpx.NewValidationError().Add("name", "The name field is required.")
	}
	if len(b.Name) > 255 {
		return httpx.NewValidationError().Add("name", "The name may not be greater than 255 characters.")
	}
	return nil
}

func (m *Module) createBank(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b bankBody
	if !httpx.BindJSON(c, &b) {
		return
	}
	if err := b.check(); err != nil {
		httpx.Fail(c, err)
		return
	}
	var id int64
	if err := m.db.Raw("INSERT INTO question_banks (course_id, name, description) VALUES (?, ?, ?) RETURNING id", cr.ID, b.Name, nullStr(b.Description)).Scan(&id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+bankJSON+" FROM question_banks b WHERE b.id = ?", id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) updateBank(c *gin.Context) {
	_, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	var b bankBody
	if !httpx.BindJSON(c, &b) {
		return
	}
	if err := b.check(); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := m.db.Exec("UPDATE question_banks SET name = ?, description = ? WHERE id = ?", b.Name, nullStr(b.Description), bid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne("SELECT "+bankJSON+" FROM question_banks b WHERE b.id = ?", bid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) deleteBank(c *gin.Context) {
	_, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	// Questions would only be detached (ON DELETE SET NULL); remove them with the bank.
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM questions WHERE question_bank_id = ?", bid).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM question_groups WHERE question_bank_id = ?", bid).Error; err != nil {
			return err
		}
		return tx.Exec("DELETE FROM question_banks WHERE id = ?", bid).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) showBank(c *gin.Context) {
	cid, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 10, 100)
	conds, args := []string{"q.question_bank_id = ?", "q.parent_question_id IS NULL"}, []any{bid} // sub-questions come nested in their passage
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds = append(conds, "(q.content ILIKE ? OR q.code::text ILIKE ?)")
		args = append(args, likeArg(s), likeArg(s))
	}
	if t := c.Query("type"); t != "" {
		conds = append(conds, "q.type = ?")
		args = append(args, t)
	}
	if d := c.Query("difficulty"); d != "" {
		conds = append(conds, "q.difficulty = ?")
		args = append(args, d)
	}
	if g := c.Query("group_id"); g == "none" {
		conds = append(conds, "q.question_group_id IS NULL")
	} else if g != "" {
		conds = append(conds, "q.question_group_id = ?")
		args = append(args, g)
	}
	where := " WHERE " + strings.Join(conds, " AND ")
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM questions q"+where, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	qs, err := m.jsonRows("SELECT "+questionJSON+" FROM questions q"+where+" ORDER BY q.id DESC LIMIT ? OFFSET ?", append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	groups, err := m.jsonRows(`SELECT jsonb_build_object('id', g.id, 'question_bank_id', g.question_bank_id, 'name', g.name, 'description', g.description,
		'position', g.position, 'question_count', (SELECT count(*) FROM questions q WHERE q.question_group_id = g.id))
		FROM question_groups g WHERE g.question_bank_id = ? AND g.course_id = ? ORDER BY g.position, g.id`, bid, cid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	bank, err := m.jsonOne("SELECT "+bankJSON+" FROM question_banks b WHERE b.id = ?", bid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bank, &obj); err != nil {
		httpx.Fail(c, err)
		return
	}
	g, _ := json.Marshal(groups)
	q, _ := json.Marshal(gin.H{"data": qs, "meta": gin.H{"page": page, "per_page": per, "total": total, "last_page": lastPage(total, per)}})
	obj["groups"], obj["questions"] = g, q
	c.JSON(http.StatusOK, obj)
}

// ---- groups --------------------------------------------------------------------

// listGroups: GET /courses/:ref/question-groups?bank_id=&q= — every group of the course with its bank and question count.
func (m *Module) listGroups(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	page, per := pageParams(c, 20, 100)
	conds, args := "g.course_id = ?", []any{cr.ID}
	if v := c.Query("bank_id"); v != "" {
		conds += " AND g.question_bank_id = ?"
		args = append(args, v)
	}
	if s := strings.TrimSpace(c.Query("q")); s != "" {
		conds += " AND (g.name ILIKE ? OR g.description ILIKE ?)"
		args = append(args, likeArg(s), likeArg(s))
	}
	var total int64
	if err := m.db.Raw("SELECT count(*) FROM question_groups g WHERE "+conds, args...).Scan(&total).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := m.jsonRows(`SELECT jsonb_build_object('id', g.id, 'question_bank_id', g.question_bank_id, 'name', g.name, 'description', g.description,
		'position', g.position, 'created_at', g.created_at,
		'bank', jsonb_build_object('id', b.id, 'name', b.name),
		'question_count', (SELECT count(*) FROM questions q WHERE q.question_group_id = g.id))
		FROM question_groups g JOIN question_banks b ON b.id = g.question_bank_id WHERE `+conds+` ORDER BY b.name, g.position, g.id LIMIT ? OFFSET ?`, append(args, per, (page-1)*per)...)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	metaPage(c, rows, total, page, per)
}

func (m *Module) createGroup(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	var b struct {
		BankID      int64   `json:"question_bank_id"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 255 {
		httpx.Fail(c, httpx.NewValidationError().Add("name", "The name field is required."))
		return
	}
	var n int64
	if err := m.db.Table("question_banks").Where("id = ? AND course_id = ?", b.BankID, cr.ID).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	if n == 0 {
		httpx.Fail(c, httpx.NewValidationError().Add("question_bank_id", "The selected question bank is invalid."))
		return
	}
	var dup int64
	_ = m.db.Raw("SELECT count(*) FROM question_groups WHERE question_bank_id = ? AND lower(name) = lower(?)", b.BankID, b.Name).Scan(&dup)
	if dup > 0 {
		httpx.Fail(c, httpx.NewValidationError().Add("name", "Tên nhóm đã tồn tại trong ngân hàng này."))
		return
	}
	var id int64
	if err := m.db.Raw(`INSERT INTO question_groups (course_id, question_bank_id, name, description, position)
		VALUES (?, ?, ?, ?, (SELECT coalesce(max(position) + 1, 0) FROM question_groups WHERE question_bank_id = ?)) RETURNING id`,
		cr.ID, b.BankID, b.Name, nullStr(b.Description), b.BankID).Scan(&id).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.jsonOne(`SELECT jsonb_build_object('id', g.id, 'question_bank_id', g.question_bank_id, 'name', g.name, 'description', g.description,
		'position', g.position, 'question_count', 0) FROM question_groups g WHERE g.id = ?`, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) deleteGroup(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	gid, ok := idParam(c, "gid")
	if !ok {
		return
	}
	res := m.db.Exec("DELETE FROM question_groups WHERE id = ? AND course_id = ?", gid, cr.ID)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

func (m *Module) updateGroup(c *gin.Context) {
	cr, _, ok := m.manageCourse(c)
	if !ok {
		return
	}
	gid, ok := idParam(c, "gid")
	if !ok {
		return
	}
	var b struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || len(b.Name) > 255 {
		httpx.Fail(c, httpx.NewValidationError().Add("name", "The name field is required."))
		return
	}
	var dup int64
	_ = m.db.Raw(`SELECT count(*) FROM question_groups g WHERE g.course_id = ? AND g.id <> ? AND lower(g.name) = lower(?)
		AND g.question_bank_id = (SELECT question_bank_id FROM question_groups WHERE id = ?)`, cr.ID, gid, b.Name, gid).Scan(&dup)
	if dup > 0 {
		httpx.Fail(c, httpx.NewValidationError().Add("name", "Tên nhóm đã tồn tại trong ngân hàng này."))
		return
	}
	res := m.db.Exec("UPDATE question_groups SET name = ?, description = ? WHERE id = ? AND course_id = ?", b.Name, nullStr(b.Description), gid, cr.ID)
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		httpx.NotFound(c, "")
		return
	}
	out, err := m.jsonOne(`SELECT jsonb_build_object('id', g.id, 'question_bank_id', g.question_bank_id, 'name', g.name, 'description', g.description,
		'position', g.position, 'question_count', (SELECT count(*) FROM questions q WHERE q.question_group_id = g.id)) FROM question_groups g WHERE g.id = ?`, gid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

// groupByName returns the id of the bank's group called name (case-insensitive), creating it when missing.
func (m *Module) groupByName(tx *gorm.DB, courseID, bankID int64, name string) (int64, error) {
	var id int64
	if err := tx.Raw("SELECT id FROM question_groups WHERE question_bank_id = ? AND lower(name) = lower(?) LIMIT 1", bankID, name).Scan(&id).Error; err != nil {
		return 0, err
	}
	if id != 0 {
		return id, nil
	}
	err := tx.Raw(`INSERT INTO question_groups (course_id, question_bank_id, name, position)
		VALUES (?, ?, ?, (SELECT coalesce(max(position) + 1, 0) FROM question_groups WHERE question_bank_id = ?)) RETURNING id`, courseID, bankID, name, bankID).Scan(&id).Error
	return id, err
}

// bulkQuestions moves several questions to a group (or out of any group) or deletes them.
func (m *Module) bulkQuestions(c *gin.Context) {
	cid, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	var b struct {
		IDs     []int64 `json:"ids"`
		Action  string  `json:"action"`
		GroupID *int64  `json:"group_id"`
	}
	if !httpx.BindJSON(c, &b) {
		return
	}
	if len(b.IDs) == 0 || len(b.IDs) > 500 || (b.Action != "move" && b.Action != "delete") {
		httpx.Fail(c, httpx.NewValidationError().Add("ids", "Chọn từ 1 đến 500 câu hỏi và một thao tác hợp lệ."))
		return
	}
	var res *gorm.DB
	if b.Action == "delete" {
		res = m.db.Exec("DELETE FROM questions WHERE question_bank_id = ? AND id IN ?", bid, b.IDs)
	} else {
		if b.GroupID != nil {
			var n int64
			_ = m.db.Table("question_groups").Where("id = ? AND question_bank_id = ? AND course_id = ?", *b.GroupID, bid, cid).Count(&n)
			if n == 0 {
				httpx.Fail(c, httpx.NewValidationError().Add("group_id", "The selected group is invalid."))
				return
			}
		}
		res = m.db.Exec("UPDATE questions SET question_group_id = ? WHERE question_bank_id = ? AND id IN ?", b.GroupID, bid, b.IDs)
	}
	if res.Error != nil {
		httpx.Fail(c, res.Error)
		return
	}
	c.JSON(http.StatusOK, gin.H{"affected": res.RowsAffected})
}

// ---- questions -----------------------------------------------------------------

// saveQuestion inserts (id == 0) or replaces a question and its answers; returns the id.
func (m *Module) saveQuestion(tx *gorm.DB, courseID, bankID, id int64, q *questionBody) (int64, error) {
	score := 1.0
	if q.DefaultScore != nil {
		score = *q.DefaultScore
	}
	if q.Type == "passage" {
		score = 0 // a passage is not scored; its sub-questions are
	}
	var meta any
	if len(q.Metadata) > 0 {
		j, _ := json.Marshal(q.Metadata)
		meta = string(j)
	}
	var group any
	if q.QuestionGroupID != nil {
		var n int64
		if err := tx.Table("question_groups").Where("id = ? AND question_bank_id = ?", *q.QuestionGroupID, bankID).Count(&n).Error; err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, httpx.NewValidationError().Add("question_group_id", "The selected group is invalid.")
		}
		group = *q.QuestionGroupID
	}
	if id == 0 {
		var parent any
		if q.ParentQuestionID != nil {
			var n int64
			if err := tx.Table("questions").Where("id = ? AND question_bank_id = ? AND type = 'passage'", *q.ParentQuestionID, bankID).Count(&n).Error; err != nil {
				return 0, err
			}
			if n == 0 {
				return 0, httpx.NewValidationError().Add("parent_question_id", "The selected passage is invalid.")
			}
			parent = *q.ParentQuestionID
		}
		err := tx.Raw(`INSERT INTO questions (code, course_id, question_bank_id, question_group_id, parent_question_id, type, content, difficulty, default_score, explanation, feedback, metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb) RETURNING id`,
			newCode(bankID), courseID, bankID, group, parent, q.Type, strings.TrimSpace(q.Content), q.Difficulty, score, nullStr(q.Explanation), nullStr(q.Feedback), meta).Scan(&id).Error
		if err != nil {
			return 0, err
		}
	} else {
		if err := tx.Exec(`UPDATE questions SET question_group_id = ?, type = ?, content = ?, difficulty = ?, default_score = ?, explanation = ?, feedback = ?, metadata = ?::jsonb WHERE id = ?`,
			group, q.Type, strings.TrimSpace(q.Content), q.Difficulty, score, nullStr(q.Explanation), nullStr(q.Feedback), meta, id).Error; err != nil {
			return 0, err
		}
		if err := tx.Exec("DELETE FROM question_answers WHERE question_id = ?", id).Error; err != nil {
			return 0, err
		}
	}
	for i, a := range q.Answers {
		if err := tx.Exec(`INSERT INTO question_answers (question_id, position, content, sub_content, correct_position, is_correct) VALUES (?, ?, ?, ?, ?, ?)`,
			id, i, strings.TrimSpace(a.Content), nullStr(a.SubContent), a.CorrectPosition, a.IsCorrect).Error; err != nil {
			return 0, err
		}
	}
	if q.Type == "passage" {
		for i := range q.Children {
			child := q.Children[i]
			child.ParentQuestionID = &id
			if _, err := m.saveQuestion(tx, courseID, bankID, 0, &child); err != nil {
				return 0, err
			}
		}
	}
	return id, nil
}

func (m *Module) questionByID(id int64) (json.RawMessage, error) {
	return m.jsonOne("SELECT "+questionJSON+" FROM questions q WHERE q.id = ?", id)
}

func (m *Module) createQuestion(c *gin.Context) {
	cid, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	var q questionBody
	if !httpx.BindJSON(c, &q) {
		return
	}
	if msg := q.check(); msg != "" {
		httpx.Fail(c, httpx.NewValidationError().Add("content", msg))
		return
	}
	var id int64
	err := m.db.Transaction(func(tx *gorm.DB) (e error) { id, e = m.saveQuestion(tx, cid, bid, 0, &q); return })
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.questionByID(id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusCreated, "", out)
}

func (m *Module) questionOf(c *gin.Context) (cid, bid, qid int64, ok bool) {
	cid, bid, ok = m.bankOf(c)
	if !ok {
		return
	}
	qid, ok = idParam(c, "qid")
	if !ok {
		return
	}
	var n int64
	if err := m.db.Table("questions").Where("id = ? AND question_bank_id = ?", qid, bid).Count(&n).Error; err != nil {
		httpx.Fail(c, err)
		return 0, 0, 0, false
	}
	if n == 0 {
		httpx.NotFound(c, "")
		return 0, 0, 0, false
	}
	return cid, bid, qid, true
}

func (m *Module) updateQuestion(c *gin.Context) {
	cid, bid, qid, ok := m.questionOf(c)
	if !ok {
		return
	}
	var q questionBody
	if !httpx.BindJSON(c, &q) {
		return
	}
	if msg := q.check(); msg != "" {
		httpx.Fail(c, httpx.NewValidationError().Add("content", msg))
		return
	}
	var cur struct {
		Type     string
		ParentID *int64
		Kids     int64
	}
	if err := m.db.Raw(`SELECT type, parent_question_id AS parent_id, (SELECT count(*) FROM questions c WHERE c.parent_question_id = questions.id) AS kids
		FROM questions WHERE id = ?`, qid).Scan(&cur).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	switch {
	case cur.ParentID != nil && q.Type == "passage":
		httpx.Fail(c, httpx.NewValidationError().Add("type", "A sub-question cannot become a passage."))
		return
	case cur.Type == "passage" && q.Type != "passage" && cur.Kids > 0:
		httpx.Fail(c, httpx.NewValidationError().Add("type", "Remove the sub-questions before changing the type of a passage."))
		return
	}
	if err := m.db.Transaction(func(tx *gorm.DB) error { _, e := m.saveQuestion(tx, cid, bid, qid, &q); return e }); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := m.questionByID(qid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	raw(c, http.StatusOK, "", out)
}

func (m *Module) deleteQuestion(c *gin.Context) {
	_, _, qid, ok := m.questionOf(c)
	if !ok {
		return
	}
	if err := m.db.Exec("DELETE FROM questions WHERE id = ?", qid).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
}

// ---- import / export -----------------------------------------------------------

func (m *Module) importQuestions(c *gin.Context) {
	cid, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	var items []parsedQuestion
	dryRun := false
	if up, isFile, err := readUpload(c); isFile {
		if err != nil {
			httpx.Fail(c, httpx.NewValidationError().Add("file", err.Error()))
			return
		}
		items = up
		dryRun = c.PostForm("dry_run") == "1" || c.PostForm("dry_run") == "true"
	} else {
		var b struct {
			DryRun    bool           `json:"dry_run"`
			Questions []questionBody `json:"questions"`
		}
		if !httpx.BindJSON(c, &b) {
			return
		}
		dryRun = b.DryRun
		for i, q := range b.Questions {
			items = append(items, parsedQuestion{Row: i + 1, Q: q})
		}
	}
	if len(items) == 0 || len(items) > 1000 {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "Cần từ 1 đến 1000 câu hỏi."))
		return
	}
	type rowErr struct {
		Row     int    `json:"row"`
		Message string `json:"message"`
	}
	errs := []rowErr{}
	for i := range items {
		msg := items[i].Err
		if msg == "" {
			msg = items[i].Q.check()
		}
		if g := items[i].Q.Group; msg == "" && g != nil && len(strings.TrimSpace(*g)) > 255 {
			msg = "Tên nhóm tối đa 255 ký tự."
		}
		if msg != "" {
			errs = append(errs, rowErr{Row: items[i].Row, Message: msg})
		}
	}
	total, valid := len(items), len(items)-len(errs)
	if dryRun {
		c.JSON(http.StatusOK, gin.H{"dry_run": true, "total": total, "valid": valid, "errors": errs})
		return
	}
	if len(errs) > 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"message": "Some questions are invalid.", "total": total, "valid": valid, "errors": errs})
		return
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		for i := range items {
			if g := items[i].Q.Group; g != nil && strings.TrimSpace(*g) != "" && items[i].Q.QuestionGroupID == nil {
				gid, e := m.groupByName(tx, cid, bid, strings.TrimSpace(*g))
				if e != nil {
					return e
				}
				items[i].Q.QuestionGroupID = &gid
			}
			if _, e := m.saveQuestion(tx, cid, bid, 0, &items[i].Q); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported": total})
}

// exportCore is a question in the shape the import accepts; exportJSON adds the sub-questions of a passage.
const exportCore = `jsonb_build_object('group', (SELECT g.name FROM question_groups g WHERE g.id = q.question_group_id), 'type', q.type, 'content', q.content, 'difficulty', q.difficulty, 'default_score', q.default_score::float8,
  'explanation', q.explanation, 'feedback', q.feedback, 'metadata', q.metadata,
  'answers', coalesce((SELECT jsonb_agg(jsonb_build_object('content', a.content, 'sub_content', a.sub_content, 'is_correct', a.is_correct,
      'correct_position', a.correct_position) ORDER BY a.position) FROM question_answers a WHERE a.question_id = q.id), '[]'::jsonb))`

var exportJSON = strings.TrimSuffix(exportCore, ")") + `, 'children', CASE WHEN q.type = 'passage' THEN coalesce((SELECT jsonb_agg(` +
	strings.ReplaceAll(exportCore, "q.", "c.") + ` ORDER BY c.id) FROM questions c WHERE c.parent_question_id = q.id), '[]'::jsonb) ELSE '[]'::jsonb END)`

// exportQuestions returns the bank's questions in the same JSON the import accepts.
func (m *Module) exportQuestions(c *gin.Context) {
	cid, bid, ok := m.bankOf(c)
	if !ok {
		return
	}
	_ = cid
	rows, err := m.jsonRows("SELECT "+exportJSON+" FROM questions q WHERE q.question_bank_id = ? AND q.parent_question_id IS NULL ORDER BY q.id", bid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if c.Query("download") != "" {
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="question-bank-%d.json"`, bid))
	}
	c.JSON(http.StatusOK, rows)
}
