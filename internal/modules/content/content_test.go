package content

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Thông báo tuyển sinh 2026":                "thong-bao-tuyen-sinh-2026",
		"D21 ngành Quản trị kinh doanh: Đẹp & Đủ!": "d21-nganh-quan-tri-kinh-doanh-dep-du",
		"  --Học viện   Công nghệ--  ":             "hoc-vien-cong-nghe",
		"CUỘC THI GEN Z x CISCO":                   "cuoc-thi-gen-z-x-cisco",
		"!!!":                                      "",
		"Đ":                                        "d",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugifyLength(t *testing.T) {
	long := ""
	for i := 0; i < 80; i++ {
		long += "chu "
	}
	if got := slugify(long); len(got) > 150 {
		t.Errorf("slug too long: %d", len(got))
	}
}

func TestSiteURLCheck(t *testing.T) {
	if len(siteURLCheck(map[string]any{"logo": "/partners/a.png", "website_url": "https://x.vn"})) != 0 {
		t.Error("valid values rejected")
	}
	if got := siteURLCheck(map[string]any{"logo": "abc"}); got["logo"] == "" {
		t.Error("invalid logo accepted")
	}
	if got := siteURLCheck(map[string]any{"logo": "/a.png", "website_url": "ftp://x"}); got["website_url"] == "" {
		t.Error("invalid website accepted")
	}
}
