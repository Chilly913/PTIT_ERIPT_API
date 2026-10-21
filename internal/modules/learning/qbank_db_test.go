package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
)

// Runs against TEST_DATABASE_URL (skipped when unset) inside a transaction that is always rolled back,
// so a development database can be used safely. The schema must already be migrated.
func TestQuestionFormatsDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	gdb, err := db.Open(context.Background(), config.DBConfig{URL: dsn, MaxOpenConns: 2, MaxIdleConns: 1, LogLevel: "silent"}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })
	m := &Module{db: tx}

	n := time.Now().UnixNano()
	u := auth.User{Email: fmt.Sprintf("qf.%d@test.local", n), Name: "QF", UserType: "instructor"}
	if err := tx.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	var courseID, bankID, otherBank int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tx.Raw("INSERT INTO courses (instructor_id, title, slug) VALUES (?, 'QF', ?) RETURNING id", u.ID, fmt.Sprintf("qf-%d", n)).Scan(&courseID).Error)
	must(tx.Raw("INSERT INTO question_banks (course_id, name) VALUES (?, 'B1') RETURNING id", courseID).Scan(&bankID).Error)
	must(tx.Raw("INSERT INTO question_banks (course_id, name) VALUES (?, 'B2') RETURNING id", courseID).Scan(&otherBank).Error)

	one := 1.0
	two := 2.0
	passage := questionBody{Type: "passage", Content: "Đọc đoạn văn sau về kinh tế số...", Difficulty: 2, Children: []questionBody{
		{Type: "single_choice", Content: "Ý chính?", Difficulty: 2, DefaultScore: &one, Answers: []answerBody{{Content: "A", IsCorrect: true}, {Content: "B"}}},
		{Type: "cloze", Content: "Kinh tế {{1}} dựa trên {{2}}.", Difficulty: 2, DefaultScore: &two, Answers: []answerBody{
			{Content: "số", SubContent: sp("1"), IsCorrect: true}, {Content: "dữ liệu", SubContent: sp("2"), IsCorrect: true}, {Content: "than đá", SubContent: sp("2")}}},
	}}
	if msg := passage.check(); msg != "" {
		t.Fatal(msg)
	}
	passageID, err := m.saveQuestion(tx, courseID, bankID, 0, &passage)
	must(err)
	single := questionBody{Type: "classification", Content: "Phân loại", Difficulty: 1, Answers: []answerBody{
		{Content: "Cá", SubContent: sp("Nước")}, {Content: "Chim", SubContent: sp("Trời")}}}
	singleID, err := m.saveQuestion(tx, courseID, bankID, 0, &single)
	must(err)

	// The JSON the web app reads: the passage carries its sub-questions, with their answers.
	raw, err := m.questionByID(passageID)
	must(err)
	var got struct {
		Type     string `json:"type"`
		Score    float64
		Children []struct {
			Type     string `json:"type"`
			ParentID int64  `json:"parent_question_id"`
			Answers  []struct {
				SubContent *string `json:"sub_content"`
			} `json:"answers"`
		} `json:"children"`
	}
	must(json.Unmarshal(raw, &got))
	if got.Type != "passage" || len(got.Children) != 2 || got.Children[0].ParentID != passageID || got.Children[1].Type != "cloze" || len(got.Children[1].Answers) != 3 {
		t.Fatalf("passage JSON: %s", raw)
	}
	if got.Children[1].Answers[0].SubContent == nil || *got.Children[1].Answers[0].SubContent != "1" {
		t.Errorf("cloze blank ids must survive: %s", raw)
	}

	// Flat Excel/Word export cannot hold a passage: only the classification question is listed.
	flat, err := m.bankQuestions(bankID)
	must(err)
	if len(flat) != 1 || flat[0].Type != "classification" {
		t.Errorf("file export should skip passages and their sub-questions, got %d questions", len(flat))
	}
	// JSON export keeps the tree.
	tree, err := m.jsonRows("SELECT "+exportJSON+" FROM questions q WHERE q.question_bank_id = ? AND q.parent_question_id IS NULL ORDER BY q.id", bankID)
	must(err)
	var shape []struct {
		Type     string
		Children []questionBody
	}
	for _, r := range tree {
		var s struct {
			Type     string
			Children []questionBody
		}
		must(json.Unmarshal(r, &s))
		shape = append(shape, s)
	}
	if len(shape) != 2 || len(shape[0].Children) != 2 {
		t.Errorf("JSON export tree: %s", tree)
	}

	// A paper picks the passage; the sub-questions follow it with their own points and answers.
	paper, err := m.expandPassages([]quizQuestion{{ID: passageID, Type: "passage", Points: 9}, {ID: singleID, Type: "classification", Points: 1}})
	must(err)
	if len(paper) != 4 || paper[0].Type != "passage" || paper[0].Points != 0 || paper[1].Type != "single_choice" || paper[2].Type != "cloze" || paper[3].ID != singleID {
		t.Fatalf("expanded paper: %+v", paper)
	}
	if paper[1].ParentID == nil || *paper[1].ParentID != passageID || paper[1].Points != 1 || paper[2].Points != 2 || len(paper[2].Answers) != 3 {
		t.Errorf("sub-questions: %+v", paper[1:3])
	}
	if presentQuestion(paper[2], 1)["parent_id"] != passageID {
		t.Error("a sub-question tells the learner which passage it belongs to")
	}

	// Structure rules.
	notPassage := questionBody{Type: "essay", Content: "x", ParentQuestionID: &singleID}
	if _, err := m.saveQuestion(tx, courseID, bankID, 0, &notPassage); err == nil {
		t.Error("a sub-question needs a passage as parent")
	}
	elsewhere := questionBody{Type: "essay", Content: "x", ParentQuestionID: &passageID}
	if _, err := m.saveQuestion(tx, courseID, otherBank, 0, &elsewhere); err == nil {
		t.Error("a sub-question must live in the passage's bank")
	}

	// The widened constraint accepts the new formats.
	if err := tx.Exec("INSERT INTO questions (code, course_id, question_bank_id, type, content) VALUES (?, ?, ?, 'file_upload', 'Nộp bài')", fmt.Sprintf("QF-%d", n), courseID, bankID).Error; err != nil {
		t.Errorf("file_upload type rejected by the database: %v", err)
	}

	// Deleting a passage deletes its sub-questions.
	must(tx.Exec("DELETE FROM questions WHERE id = ?", passageID).Error)
	var left int64
	must(tx.Raw("SELECT count(*) FROM questions WHERE parent_question_id = ?", passageID).Scan(&left).Error)
	if left != 0 {
		t.Errorf("%d orphan sub-questions", left)
	}
}
