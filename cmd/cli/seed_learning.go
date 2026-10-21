package main

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

type seedLesson struct {
	Title    string
	Type     string
	Video    string
	Duration int
	Preview  bool
}

type seedSection struct {
	Title   string
	Lessons []seedLesson
}

type seedCourse struct {
	Slug, Title, Category, Level, Status string
	Description                          string
	Price                                int
	Sections                             []seedSection
}

var seedCategories = []struct{ Slug, Name string }{
	{"kinh-te-so", "Kinh tế số"},
	{"thuong-mai-dien-tu", "Thương mại điện tử"},
	{"tai-chinh-ngan-hang", "Tài chính - Ngân hàng"},
	{"quan-tri-kinh-doanh", "Quản trị kinh doanh"},
}

const demoVideo = "https://www.youtube.com/watch?v=aqz-KE-bpKQ"

var seedCourses = []seedCourse{
	{
		Slug: "nhap-mon-kinh-te-so", Title: "Nhập môn Kinh tế số", Category: "kinh-te-so", Level: "beginner", Status: "published",
		Description: "Tổng quan về nền kinh tế số, mô hình kinh doanh số và các xu hướng chuyển đổi số trong doanh nghiệp Việt Nam.",
		Sections: []seedSection{
			{"Khái niệm cơ bản", []seedLesson{{"Kinh tế số là gì?", "video", demoVideo, 600, true}, {"Các thành phần của kinh tế số", "video", demoVideo, 840, false}}},
			{"Chuyển đổi số doanh nghiệp", []seedLesson{{"Lộ trình chuyển đổi số", "video", demoVideo, 720, false}, {"Tài liệu đọc thêm", "page", "", 300, false}}},
		},
	},
	{
		Slug: "thuong-mai-dien-tu-can-ban", Title: "Thương mại điện tử căn bản", Category: "thuong-mai-dien-tu", Level: "beginner", Status: "published",
		Description: "Các mô hình thương mại điện tử, vận hành gian hàng trực tuyến và đo lường hiệu quả bán hàng.",
		Sections: []seedSection{
			{"Mô hình thương mại điện tử", []seedLesson{{"B2B, B2C, C2C", "video", demoVideo, 540, true}, {"Hệ sinh thái sàn TMĐT", "video", demoVideo, 660, false}}},
			{"Vận hành gian hàng", []seedLesson{{"Quy trình xử lý đơn hàng", "video", demoVideo, 480, false}}},
		},
	},
	{
		Slug: "tai-chinh-ca-nhan", Title: "Tài chính cá nhân", Category: "tai-chinh-ngan-hang", Level: "intermediate", Status: "published",
		Description: "Lập ngân sách, tiết kiệm, đầu tư cơ bản và quản trị rủi ro tài chính cá nhân.",
		Sections: []seedSection{
			{"Nền tảng", []seedLesson{{"Lập ngân sách cá nhân", "video", demoVideo, 600, true}, {"Quỹ khẩn cấp", "video", demoVideo, 420, false}}},
		},
	},
	{
		Slug: "quan-tri-du-an-cntt", Title: "Quản trị dự án công nghệ thông tin", Category: "quan-tri-kinh-doanh", Level: "advanced", Status: "pending_review",
		Description: "Phương pháp Agile, Scrum và quản trị rủi ro trong dự án CNTT. Khoá đang chờ duyệt.",
		Sections: []seedSection{
			{"Agile và Scrum", []seedLesson{{"Giới thiệu Scrum", "video", demoVideo, 900, false}}},
		},
	},
}

// seedLearning upserts categories, sample courses (owned by the demo instructor) and a demo enrolment.
func seedLearning() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.App.IsProduction() {
		return errors.New("seed: refusing to seed demo courses when APP_ENV=production")
	}
	gdb, err := db.Open(context.Background(), cfg.DB, logx.New(cfg.Log, cfg.App))
	if err != nil {
		return err
	}
	if sqlDB, e := gdb.DB(); e == nil {
		defer sqlDB.Close()
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		var instructor, student int64
		tx.Raw("SELECT id FROM users WHERE email = 'instructor@eript.test'").Scan(&instructor)
		tx.Raw("SELECT id FROM users WHERE email = 'student@eript.test'").Scan(&student)
		if instructor == 0 || student == 0 {
			return errors.New("seed learning: run `seed users` first")
		}
		for i, c := range seedCategories {
			if err := tx.Exec(`INSERT INTO categories (slug, name, sort_order) VALUES (?, ?, ?)
				ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name`, c.Slug, c.Name, i).Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("UPDATE lessons SET video_path = ? WHERE video_path LIKE '%dQw4w9WgXcQ%'", demoVideo).Error; err != nil {
			return err
		}
		for _, c := range seedCourses {
			var id int64
			if err := tx.Raw(`INSERT INTO courses (instructor_id, category_id, title, slug, description, level, price, status, published_at)
				VALUES (?, (SELECT id FROM categories WHERE slug = ?), ?, ?, ?, ?, ?, ?, CASE WHEN ? = 'published' THEN now() END)
				ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title RETURNING id`,
				instructor, c.Category, c.Title, c.Slug, c.Description, c.Level, c.Price, c.Status, c.Status).Scan(&id).Error; err != nil {
				return fmt.Errorf("course %s: %w", c.Slug, err)
			}
			var existing int64
			tx.Raw("SELECT count(*) FROM sections WHERE course_id = ?", id).Scan(&existing)
			if existing > 0 {
				continue
			}
			pos := 0
			for si, s := range c.Sections {
				var sid int64
				if err := tx.Raw("INSERT INTO sections (course_id, title, position) VALUES (?, ?, ?) RETURNING id", id, s.Title, si).Scan(&sid).Error; err != nil {
					return err
				}
				for _, l := range s.Lessons {
					var video any
					if l.Video != "" {
						video = l.Video
					}
					if err := tx.Exec(`INSERT INTO lessons (course_id, section_id, title, type, position, duration_seconds, is_preview, video_path, video_status)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ready')`, id, sid, l.Title, l.Type, pos, l.Duration, l.Preview, video).Error; err != nil {
						return fmt.Errorf("lesson %s: %w", l.Title, err)
					}
					pos++
				}
			}
		}
		if err := tx.Exec(`INSERT INTO enrollments (user_id, course_id) SELECT ?, id FROM courses WHERE slug = 'nhap-mon-kinh-te-so'
			ON CONFLICT (user_id, course_id) DO NOTHING`, student).Error; err != nil {
			return err
		}
		// One class section so the gradebook has something to open; the demo student sits in it.
		var secID int64
		if err := tx.Raw(`INSERT INTO class_sections (course_id, lecturer_id, code, name, status)
			SELECT id, ?, 'KTS-01', 'Lớp Kinh tế số 01', 'open' FROM courses WHERE slug = 'nhap-mon-kinh-te-so'
			ON CONFLICT (course_id, term_id, code) DO UPDATE SET lecturer_id = EXCLUDED.lecturer_id RETURNING id`, instructor).Scan(&secID).Error; err != nil {
			return fmt.Errorf("class section: %w", err)
		}
		if err := tx.Exec(`UPDATE enrollments SET class_section_id = ? WHERE user_id = ? AND course_id = (SELECT id FROM courses WHERE slug = 'nhap-mon-kinh-te-so')`, secID, student).Error; err != nil {
			return err
		}
		fmt.Printf("seeded %d categories, %d courses (student@eript.test enrolled in nhap-mon-kinh-te-so)\n", len(seedCategories), len(seedCourses))
		return nil
	})
}
