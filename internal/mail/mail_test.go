package mail

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

func TestNewDrivers(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if m, err := New(config.MailConfig{Mailer: "log"}, log); err != nil || m.(*LogMailer) == nil {
		t.Fatal("log driver")
	}
	if m, err := New(config.MailConfig{Mailer: "smtp"}, log); err != nil || m.(*SMTPMailer) == nil {
		t.Fatal("smtp driver")
	}
	if _, err := New(config.MailConfig{Mailer: "ses"}, log); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestTemplatesAndBuild(t *testing.T) {
	url := "http://localhost:3000/reset-password?token=abc&email=a%40b.vn"
	msg, err := PasswordReset("ERIPT", "a@b.vn", "Nguyễn Văn A", url, 60)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Subject != "Đặt lại mật khẩu" || !strings.Contains(msg.Text, url) || !strings.Contains(msg.Text, "60 phút") {
		t.Fatalf("text: %s", msg.Text)
	}
	if !strings.Contains(msg.HTML, "token=abc&amp;email=a%40b.vn") {
		t.Fatalf("html must contain escaped url: %s", msg.HTML)
	}
	raw, err := Build("noreply@lms.local", "ERIPT", msg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"Subject: =?utf-8?q?", "multipart/alternative", "text/plain; charset=UTF-8", "text/html; charset=UTF-8", "To: =?utf-8?q?Nguy"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if _, err := Build("x@y", "", Message{To: "not an address"}); err == nil {
		t.Fatal("invalid recipient accepted")
	}

	v, err := VerifyEmail("ERIPT", "a@b.vn", "", "http://x/verify-email?id=1", 60)
	if err != nil || v.Subject != "Xác nhận địa chỉ email" || !strings.Contains(v.Text, "http://x/verify-email?id=1") {
		t.Fatalf("verify mail: %+v %v", v, err)
	}

	var mem Memory
	_ = mem.Send(context.Background(), v)
	if got, ok := mem.Last("A@B.VN"); !ok || got.Subject != v.Subject {
		t.Fatal("memory mailer")
	}
}
