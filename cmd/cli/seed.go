package main

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// demoPassword is shared by every demo account. Local development only.
const demoPassword = "Demo@1234"

type demoUser struct {
	Email    string
	Name     string
	UserType string // users.user_type (ck_users_user_type)
	Role     string // roles.name
	Staff    string // staff_code, empty for none
	Student  string // student_code, empty for none
}

var demoUsers = []demoUser{
	{Email: "admin@eript.test", Name: "Quản trị viên", UserType: "admin", Role: "admin", Staff: "AD001"},
	{Email: "academic@eript.test", Name: "Quản lý đào tạo", UserType: "academic_manager", Role: "academic_manager", Staff: "QL001"},
	{Email: "instructor@eript.test", Name: "Giảng viên Demo", UserType: "instructor", Role: "instructor", Staff: "GV001"},
	{Email: "advisor@eript.test", Name: "Cố vấn học tập", UserType: "staff", Role: "advisor", Staff: "CV001"},
	{Email: "finance@eript.test", Name: "Cán bộ tài chính", UserType: "staff", Role: "finance", Staff: "TC001"},
	{Email: "student@eript.test", Name: "Sinh viên Demo", UserType: "student", Role: "student", Student: "SV001"},
}

// seedCmd handles `cli seed users`: idempotent demo accounts, one per role.
func seedCmd(args []string) error {
	if len(args) > 0 && args[0] == "content" {
		return seedContent()
	}
	if len(args) > 0 && args[0] == "learning" {
		return seedLearning()
	}
	if len(args) > 0 && args[0] == "academic" {
		return seedAcademic()
	}
	if len(args) == 0 || args[0] != "users" {
		return errors.New("seed: usage: seed users|content|academic")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.App.Env == "production" {
		return errors.New("seed: refusing to create demo accounts when APP_ENV=production")
	}
	ctx := context.Background()
	gdb, err := db.Open(ctx, cfg.DB, logx.New(cfg.Log, cfg.App))
	if err != nil {
		return err
	}
	if sqlDB, e := gdb.DB(); e == nil {
		defer sqlDB.Close()
	}

	hash, err := auth.HashPassword(demoPassword)
	if err != nil {
		return err
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		for _, u := range demoUsers {
			if err := upsertDemoUser(tx, u, hash); err != nil {
				return fmt.Errorf("%s: %w", u.Email, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("seeded %d demo users (password: %s)\n", len(demoUsers), demoPassword)
	for _, u := range demoUsers {
		fmt.Printf("  %-8s %s\n", u.Role, u.Email)
	}
	return nil
}

func upsertDemoUser(tx *gorm.DB, u demoUser, hash string) error {
	var roleID int64
	if err := tx.Raw(`SELECT id FROM roles WHERE name = ?`, u.Role).Scan(&roleID).Error; err != nil {
		return err
	}
	if roleID == 0 {
		return fmt.Errorf("role %q not found (run migrate up first)", u.Role)
	}

	staff := nullable(u.Staff)
	student := nullable(u.Student)

	var userID int64
	if err := tx.Raw(`SELECT id FROM users WHERE email = ? AND deleted_at IS NULL`, u.Email).Scan(&userID).Error; err != nil {
		return err
	}
	if userID == 0 {
		if err := tx.Raw(`
			INSERT INTO users (email, password_hash, email_verified_at, name, user_type, staff_code, student_code)
			VALUES (?, ?, now(), ?, ?, ?, ?) RETURNING id`,
			u.Email, hash, u.Name, u.UserType, staff, student).Scan(&userID).Error; err != nil {
			return err
		}
	} else if err := tx.Exec(`
		UPDATE users SET password_hash = ?, email_verified_at = COALESCE(email_verified_at, now()),
			name = ?, user_type = ?, disabled_at = NULL, updated_at = now()
		WHERE id = ?`, hash, u.Name, u.UserType, userID).Error; err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, userID, roleID).Error
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
