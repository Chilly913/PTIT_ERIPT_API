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

// seedAcademic upserts a small academic structure for the Viện Kinh tế Bưu điện (dev only).
func seedAcademic() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.App.IsProduction() {
		return errors.New("seed: refusing to seed demo data when APP_ENV=production")
	}
	gdb, err := db.Open(context.Background(), cfg.DB, logx.New(cfg.Log, cfg.App))
	if err != nil {
		return err
	}
	if sqlDB, e := gdb.DB(); e == nil {
		defer sqlDB.Close()
	}

	return gdb.Transaction(func(tx *gorm.DB) error {
		var inst int64
		if err := tx.Raw(`INSERT INTO institutions (code, name, institution_type) VALUES ('PTIT', 'Học viện Công nghệ Bưu chính Viễn thông', 'academy')
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&inst).Error; err != nil {
			return err
		}
		exec := func(sql string, args ...any) error {
			if err := tx.Exec(sql, args...).Error; err != nil {
				return fmt.Errorf("%s: %w", sql[:40], err)
			}
			return nil
		}

		types := [][3]string{
			{"DHCQ", "Đại học chính quy", "Hệ đào tạo đại học chính quy tập trung"},
			{"TXLD", "Từ xa cho người lao động", "Đào tạo từ xa dành cho người đang đi làm"},
			{"VLVH", "Vừa làm vừa học", "Đại học vừa làm vừa học"},
			{"NGANHAN", "Đào tạo ngắn hạn", "Các khóa bồi dưỡng, chứng chỉ ngắn hạn"},
		}
		for _, t := range types {
			if err := exec(`INSERT INTO program_types (code, name, description) VALUES (?, ?, ?)
				ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description`, t[0], t[1], t[2]); err != nil {
				return err
			}
		}

		programs := []struct {
			typ, code, name string
			months          int
		}{
			{"DHCQ", "QTKD", "Quản trị kinh doanh", 48},
			{"DHCQ", "CNTT", "Công nghệ thông tin", 48},
			{"DHCQ", "DTVT", "Kỹ thuật điện tử viễn thông", 48},
			{"DHCQ", "MKT-CLC", "Marketing (chất lượng cao)", 48},
			{"VLVH", "QTKD-VLVH", "Quản trị kinh doanh (vừa làm vừa học)", 54},
		}
		for _, p := range programs {
			if err := exec(`INSERT INTO programs (institution_id, program_type_id, code, name, duration_months)
				VALUES (?, (SELECT id FROM program_types WHERE code = ?), ?, ?, ?)
				ON CONFLICT (institution_id, code) DO UPDATE SET name = EXCLUDED.name, duration_months = EXCLUDED.duration_months`,
				inst, p.typ, p.code, p.name, p.months); err != nil {
				return err
			}
		}

		for _, y := range []struct {
			name, from, to, status string
			current                bool
		}{
			{"2025-2026", "2025-09-01", "2026-08-31", "completed", false},
			{"2026-2027", "2026-09-01", "2027-08-31", "active", true},
		} {
			if err := exec(`INSERT INTO academic_years (institution_id, name, start_date, end_date, status, is_current)
				VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (institution_id, name) DO UPDATE SET status = EXCLUDED.status`,
				inst, y.name, y.from, y.to, y.status, y.current); err != nil {
				return err
			}
		}
		for _, t := range []struct {
			year, code, name, from, to, status string
			current                            bool
		}{
			{"2026-2027", "HK1-2627", "Học kỳ 1 năm học 2026-2027", "2026-09-01", "2027-01-15", "ongoing", true},
			{"2026-2027", "HK2-2627", "Học kỳ 2 năm học 2026-2027", "2027-02-01", "2027-06-15", "upcoming", false},
			{"2025-2026", "HK1-2526", "Học kỳ 1 năm học 2025-2026", "2025-09-01", "2026-01-15", "completed", false},
			{"2025-2026", "HK2-2526", "Học kỳ 2 năm học 2025-2026", "2026-02-01", "2026-06-15", "completed", false},
		} {
			if err := exec(`INSERT INTO terms (academic_year_id, code, name, start_date, end_date, status, is_current)
				VALUES ((SELECT id FROM academic_years WHERE institution_id = ? AND name = ?), ?, ?, ?, ?, ?, ?)
				ON CONFLICT (academic_year_id, code) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status`,
				inst, t.year, t.code, t.name, t.from, t.to, t.status, t.current); err != nil {
				return err
			}
		}

		for _, c := range []struct {
			prog, code, name string
			start, end       int
			status           string
		}{
			{"QTKD", "D21", "Khóa 2021 – Quản trị kinh doanh", 2021, 2025, "graduated"},
			{"QTKD", "D22", "Khóa 2022 – Quản trị kinh doanh", 2022, 2026, "active"},
			{"QTKD", "D23", "Khóa 2023 – Quản trị kinh doanh", 2023, 2027, "active"},
			{"CNTT", "D21CN", "Khóa 2021 – Công nghệ thông tin", 2021, 2025, "graduated"},
			{"CNTT", "D24CN", "Khóa 2024 – Công nghệ thông tin", 2024, 2028, "active"},
			{"DTVT", "D24DT", "Khóa 2024 – Kỹ thuật điện tử viễn thông", 2024, 2028, "active"},
		} {
			if err := exec(`INSERT INTO cohorts (institution_id, program_id, code, name, start_year, end_year, status)
				VALUES (?, (SELECT id FROM programs WHERE institution_id = ? AND code = ?), ?, ?, ?, ?, ?)
				ON CONFLICT (program_id, code) DO UPDATE SET name = EXCLUDED.name, status = EXCLUDED.status`,
				inst, inst, c.prog, c.code, c.name, c.start, c.end, c.status); err != nil {
				return err
			}
		}

		for _, c := range []struct {
			prog, cohort, code, name string
			grad, cap                int
			status                   string
		}{
			{"QTKD", "D22", "D22QTKD01", "D22QTKD01", 2026, 60, "active"},
			{"QTKD", "D22", "D22QTKD02", "D22QTKD02", 2026, 58, "active"},
			{"QTKD", "D23", "D23QTKD01", "D23QTKD01", 2027, 62, "active"},
			{"CNTT", "D24CN", "D24CNTT01", "D24CNTT01", 2028, 55, "active"},
			{"DTVT", "D24DT", "D24DTVT01", "D24DTVT01", 2028, 50, "active"},
			{"QTKD", "D21", "D21QTKD01", "D21QTKD01", 2025, 60, "graduated"},
		} {
			if err := exec(`INSERT INTO administrative_classes (institution_id, program_id, cohort_id, code, name, expected_graduation_year, capacity, status)
				VALUES (?, (SELECT id FROM programs WHERE institution_id = ? AND code = ?),
					(SELECT id FROM cohorts WHERE program_id = (SELECT id FROM programs WHERE institution_id = ? AND code = ?) AND code = ?), ?, ?, ?, ?, ?)
				ON CONFLICT (institution_id, code) DO UPDATE SET name = EXCLUDED.name, capacity = EXCLUDED.capacity, status = EXCLUDED.status`,
				inst, inst, c.prog, inst, c.prog, c.cohort, c.code, c.name, c.grad, c.cap, c.status); err != nil {
				return err
			}
		}
		// Give the active demo classes the demo advisor when that account exists (see `seed users`).
		if err := exec(`UPDATE administrative_classes SET advisor_id = (SELECT id FROM users WHERE email = 'advisor@eript.test' AND deleted_at IS NULL)
			WHERE advisor_id IS NULL AND status = 'active'`); err != nil {
			return err
		}
		fmt.Println("seeded academic data: 4 program types, 5 programs, 2 academic years, 4 terms, 6 cohorts, 6 classes")
		return nil
	})
}
