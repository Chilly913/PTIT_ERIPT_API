package main

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/db"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

// adminCmd handles `cli admin <email> [name]`: creates (or resets) an administrator account. Unlike `seed users` it
// works in production, so a fresh deployment can get its first login. The password is read from ADMIN_PASSWORD
// (never from the command line, which ends up in shell history and process lists).
func adminCmd(args []string) error {
	if len(args) < 1 {
		return errors.New("admin: usage: ADMIN_PASSWORD=... cli admin <email> [full name]")
	}
	email := strings.ToLower(strings.TrimSpace(args[0]))
	if _, err := mail.ParseAddress(email); err != nil {
		return fmt.Errorf("admin: %q is not a valid email", email)
	}
	name := "Quản trị viên"
	if len(args) > 1 && strings.TrimSpace(strings.Join(args[1:], " ")) != "" {
		name = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	password := os.Getenv("ADMIN_PASSWORD")
	if utf8.RuneCountInString(password) < 10 {
		return errors.New("admin: set ADMIN_PASSWORD to at least 10 characters")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	gdb, err := db.Open(ctx, cfg.DB, logx.New(cfg.Log, cfg.App))
	if err != nil {
		return err
	}
	if sqlDB, e := gdb.DB(); e == nil {
		defer sqlDB.Close()
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	err = gdb.Transaction(func(tx *gorm.DB) error {
		return upsertDemoUser(tx, demoUser{Email: email, Name: name, UserType: "admin", Role: "admin", Staff: ""}, hash)
	})
	if err != nil {
		return err
	}
	fmt.Printf("administrator ready: %s\n", email)
	return nil
}
