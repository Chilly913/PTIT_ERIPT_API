package mail

import (
	"bytes"
	"fmt"
	"html/template"
)

// ActionMail is the data of a one-button notification e-mail (the layout of
// Laravel's MailMessage: greeting, intro lines, action button, outro lines).
type ActionMail struct {
	AppName     string
	Greeting    string
	IntroLines  []string
	ActionText  string
	ActionURL   string
	OutroLines  []string
	Salutation  string
	FallbackTip string
}

var actionTmpl = template.Must(template.New("action").Parse(`<!doctype html>
<html lang="vi"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.AppName}}</title></head>
<body style="margin:0;padding:24px;background:#f4f5f7;font-family:Arial,Helvetica,sans-serif;color:#1f2937">
<table role="presentation" width="100%" cellspacing="0" cellpadding="0"><tr><td align="center">
<table role="presentation" width="560" cellspacing="0" cellpadding="0" style="max-width:560px;width:100%;background:#ffffff;border-radius:8px;padding:32px">
<tr><td>
<h1 style="font-size:20px;margin:0 0 16px">{{.Greeting}}</h1>
{{range .IntroLines}}<p style="font-size:15px;line-height:1.6;margin:0 0 12px">{{.}}</p>{{end}}
<p style="text-align:center;margin:24px 0"><a href="{{.ActionURL}}" style="display:inline-block;background:#b91c1c;color:#ffffff;text-decoration:none;padding:12px 24px;border-radius:6px;font-weight:bold">{{.ActionText}}</a></p>
{{range .OutroLines}}<p style="font-size:15px;line-height:1.6;margin:0 0 12px">{{.}}</p>{{end}}
<p style="font-size:15px;line-height:1.6;margin:16px 0 0">{{.Salutation}}<br>{{.AppName}}</p>
<hr style="border:none;border-top:1px solid #e5e7eb;margin:24px 0">
<p style="font-size:12px;line-height:1.5;color:#6b7280;margin:0">{{.FallbackTip}}<br><a href="{{.ActionURL}}" style="color:#6b7280;word-break:break-all">{{.ActionURL}}</a></p>
</td></tr></table>
</td></tr></table>
</body></html>`))

// Render returns the text and HTML bodies.
func (a ActionMail) Render() (text, html string, err error) {
	var t bytes.Buffer
	fmt.Fprintf(&t, "%s\n\n", a.Greeting)
	for _, l := range a.IntroLines {
		fmt.Fprintf(&t, "%s\n\n", l)
	}
	fmt.Fprintf(&t, "%s: %s\n\n", a.ActionText, a.ActionURL)
	for _, l := range a.OutroLines {
		fmt.Fprintf(&t, "%s\n\n", l)
	}
	fmt.Fprintf(&t, "%s\n%s\n", a.Salutation, a.AppName)

	var h bytes.Buffer
	if err := actionTmpl.Execute(&h, a); err != nil {
		return "", "", err
	}
	return t.String(), h.String(), nil
}

func actionMessage(to, toName, subject string, a ActionMail) (Message, error) {
	text, html, err := a.Render()
	if err != nil {
		return Message{}, err
	}
	return Message{To: to, ToName: toName, Subject: subject, Text: text, HTML: html}, nil
}

// PasswordReset builds the reset-password e-mail (Laravel ResetPassword
// notification, in Vietnamese).
func PasswordReset(appName, to, toName, url string, expireMinutes int) (Message, error) {
	return actionMessage(to, toName, "Đặt lại mật khẩu", ActionMail{
		AppName:  appName,
		Greeting: "Xin chào!",
		IntroLines: []string{
			"Bạn nhận được email này vì chúng tôi đã nhận được yêu cầu đặt lại mật khẩu cho tài khoản của bạn.",
		},
		ActionText: "Đặt lại mật khẩu",
		ActionURL:  url,
		OutroLines: []string{
			fmt.Sprintf("Liên kết đặt lại mật khẩu sẽ hết hạn sau %d phút.", expireMinutes),
			"Nếu bạn không yêu cầu đặt lại mật khẩu, bạn không cần làm gì thêm.",
		},
		Salutation:  "Trân trọng,",
		FallbackTip: "Nếu bạn không bấm được nút \"Đặt lại mật khẩu\", hãy sao chép và dán đường dẫn sau vào trình duyệt:",
	})
}

// VerifyEmail builds the e-mail verification message (Laravel VerifyEmail
// notification, in Vietnamese).
func VerifyEmail(appName, to, toName, url string, expireMinutes int) (Message, error) {
	return actionMessage(to, toName, "Xác nhận địa chỉ email", ActionMail{
		AppName:  appName,
		Greeting: "Xin chào!",
		IntroLines: []string{
			"Vui lòng bấm nút bên dưới để xác nhận địa chỉ email của bạn.",
		},
		ActionText: "Xác nhận email",
		ActionURL:  url,
		OutroLines: []string{
			fmt.Sprintf("Liên kết xác nhận sẽ hết hạn sau %d phút.", expireMinutes),
			"Nếu bạn không tạo tài khoản, bạn không cần làm gì thêm.",
		},
		Salutation:  "Trân trọng,",
		FallbackTip: "Nếu bạn không bấm được nút \"Xác nhận email\", hãy sao chép và dán đường dẫn sau vào trình duyệt:",
	})
}
