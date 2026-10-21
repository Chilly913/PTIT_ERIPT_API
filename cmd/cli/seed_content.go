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

type seedPartner struct{ Name, Logo, URL string }

// Partner logos are served as static assets by the web app (eript-web/public/partners).
var seedPartners = []seedPartner{
	{"Tập đoàn Điện lực Việt Nam (EVN)", "/partners/evn.png", ""},
	{"Tập đoàn FPT", "/partners/fpt.png", "https://fpt.com"},
	{"Tập đoàn Viettel", "/partners/viettel.jpg", "https://viettel.com.vn"},
	{"VNPT", "/partners/vnpt.png", "https://vnpt.com.vn"},
	{"Samsung", "/partners/samsung.png", "https://www.samsung.com/vn"},
	{"OnePay", "/partners/onepay.svg", "https://www.onepay.vn"},
	{"MB Bank", "/partners/mb.webp", "https://www.mbbank.com.vn"},
	{"Techcombank", "/partners/techcombank.webp", "https://techcombank.com"},
	{"VinFast", "/partners/vinfast.png", "https://vinfastauto.com"},
	{"TPBank", "/partners/tpbank.jpg", "https://tpb.vn"},
	{"Vietcombank", "/partners/vietcombank.webp", "https://www.vietcombank.com.vn"},
	{"Viglacera", "/partners/viglacera.png", "https://viglacera.com.vn"},
}

// seedSettings are non-secret public site settings (key -> value).
var seedSettings = map[string]string{
	"site_name":       "Viện Kinh tế Bưu điện",
	"site_logo":       "/brand/eript-logo.png",
	"footer_text":     "Viện Kinh tế Bưu điện (ERIPT) thuộc Học viện Công nghệ Bưu chính Viễn thông: đào tạo, nghiên cứu và hợp tác trong lĩnh vực kinh tế, quản trị và công nghệ số.",
	"contact_email":   "eript@ptit.edu.vn",
	"contact_phone":   "024-3574-6799",
	"contact_address": "Số 122 Hoàng Quốc Việt, phường Nghĩa Đô, thành phố Hà Nội",
	"social_links":    `{"website":"https://eript.ptit.edu.vn"}`,
}

// seedContent upserts the public content of the Viện Kinh tế Bưu điện site: news posts, partners, settings.
func seedContent() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.App.IsProduction() {
		return errors.New("seed: refusing to seed demo content when APP_ENV=production")
	}
	ctx := context.Background()
	gdb, err := db.Open(ctx, cfg.DB, logx.New(cfg.Log, cfg.App))
	if err != nil {
		return err
	}
	if sqlDB, e := gdb.DB(); e == nil {
		defer sqlDB.Close()
	}

	err = gdb.Transaction(func(tx *gorm.DB) error {
		for k, v := range seedSettings {
			if err := tx.Exec(`INSERT INTO site_settings (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, value_encrypted = NULL`, k, v).Error; err != nil {
				return fmt.Errorf("setting %s: %w", k, err)
			}
		}
		for i, p := range seedPartners {
			var url any
			if p.URL != "" {
				url = p.URL
			}
			if err := tx.Exec(`INSERT INTO partners (name, logo_path, website_url, sort_order) VALUES (?, ?, ?, ?)
				ON CONFLICT (name) DO UPDATE SET logo_path = EXCLUDED.logo_path, website_url = EXCLUDED.website_url,
					sort_order = EXCLUDED.sort_order, is_active = true`, p.Name, p.Logo, url, i+1).Error; err != nil {
				return fmt.Errorf("partner %s: %w", p.Name, err)
			}
		}
		for _, n := range seedPosts {
			var cover any
			if n.Cover != "" {
				cover = n.Cover
			}
			if err := tx.Exec(`INSERT INTO news_posts (title, slug, excerpt, content, cover_image_path, status, is_featured, published_at)
				VALUES (?, ?, ?, ?, ?, 'published', ?, ?)
				ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title, excerpt = EXCLUDED.excerpt, content = EXCLUDED.content,
					cover_image_path = EXCLUDED.cover_image_path, is_featured = EXCLUDED.is_featured,
					published_at = EXCLUDED.published_at, status = 'published'`,
				n.Title, n.Slug, n.Excerpt, n.Content, cover, n.Featured, n.PublishedAt).Error; err != nil {
				return fmt.Errorf("post %s: %w", n.Slug, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("seeded content: %d settings, %d partners, %d news posts\n", len(seedSettings), len(seedPartners), len(seedPosts))
	return nil
}
