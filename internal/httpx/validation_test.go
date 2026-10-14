package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

type item struct {
	Name string `json:"name" binding:"required"`
}

type registerReq struct {
	FullName             string `json:"full_name" binding:"required,max=10"`
	Email                string `json:"email" binding:"required,email"`
	Password             string `json:"password" binding:"required,min=8"`
	PasswordConfirmation string `json:"password_confirmation" binding:"eqfield=Password"`
	Age                  int    `json:"age" binding:"omitempty,min=18"`
	Role                 string `json:"role" binding:"omitempty,oneof=student instructor"`
	Phone                string `json:"phone" binding:"required_if=Role instructor"`
	Items                []item `json:"items" binding:"omitempty,max=2,dive"`
}

func run(t *testing.T, body string, dst any, bind func(*gin.Context, any) bool) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/x", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ok := bind(c, dst)
	return w, ok
}

type errBody struct {
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) errBody {
	t.Helper()
	var b errBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("bad json %q: %v", w.Body.String(), err)
	}
	return b
}

func TestBindJSONEmptyBodyRequiredErrors(t *testing.T) {
	var req registerReq
	w, ok := run(t, "", &req, BindJSON)
	if ok || w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d ok=%v", w.Code, ok)
	}
	b := decode(t, w)
	if b.Message != "The full name field is required. (and 2 more errors)" {
		t.Errorf("message = %q", b.Message)
	}
	if got := b.Errors["email"]; len(got) != 1 || got[0] != "The email field is required." {
		t.Errorf("email errors = %v", got)
	}
	// insertion order is struct order
	if !strings.HasPrefix(w.Body.String(), `{"message":"The full name field is required. (and 2 more errors)","errors":{"full_name":`) {
		t.Errorf("unexpected body order: %s", w.Body.String())
	}
}

func TestBindJSONRuleMessages(t *testing.T) {
	var req registerReq
	body := `{"full_name":"  This name is far too long ","email":"nope","password":"short","password_confirmation":"other",
		"age":12,"role":"admin","items":[{"name":""},{"name":"a"},{"name":"b"}]}`
	w, _ := run(t, body, &req, BindJSON)
	b := decode(t, w)
	want := map[string]string{
		"full_name": "The full name field must not be greater than 10 characters.",
		"email":     "The email field must be a valid email address.",
		"password":  "The password field must be at least 8 characters.",
		"age":       "The age field must be at least 18.",
		"role":      "The selected role is invalid.",
		"items":     "The items field must not have more than 2 items.",
	}
	for f, m := range want {
		if got := b.Errors[f]; len(got) == 0 || got[0] != m {
			t.Errorf("%s: got %v, want %q", f, got, m)
		}
	}
	// confirmed: reported on "password" as a second message
	if got := b.Errors["password"]; len(got) != 2 || got[1] != "The password field confirmation does not match." {
		t.Errorf("password errors = %v", got)
	}
	if _, ok := b.Errors["password_confirmation"]; ok {
		t.Errorf("confirmation error must be reported on password")
	}
}

func TestBindJSONNestedAndRequiredIf(t *testing.T) {
	var req registerReq
	body := `{"full_name":"A","email":"a@b.co","password":"12345678","password_confirmation":"12345678","role":"instructor","items":[{"name":"x"},{"name":""}]}`
	w, _ := run(t, body, &req, BindJSON)
	b := decode(t, w)
	if got := b.Errors["phone"]; len(got) != 1 || got[0] != "The phone field is required when role is instructor." {
		t.Errorf("phone = %v", got)
	}
	if got := b.Errors["items.1.name"]; len(got) != 1 || got[0] != "The items.1.name field is required." {
		t.Errorf("items.1.name = %v (all %v)", got, b.Errors)
	}
	if b.Message != "The phone field is required when role is instructor. (and 1 more error)" {
		t.Errorf("message = %q", b.Message)
	}
}

func TestBindJSONSuccessTrimsStrings(t *testing.T) {
	var req registerReq
	body := `{"full_name":"  Huy  ","email":"a@b.co","password":" 12345678 ","password_confirmation":" 12345678 "}`
	_, ok := run(t, body, &req, BindJSON)
	if !ok {
		t.Fatal("expected success")
	}
	if req.FullName != "Huy" || req.Password != " 12345678 " {
		t.Errorf("trim: full_name=%q password=%q", req.FullName, req.Password)
	}
}

func TestBindJSONTypeError(t *testing.T) {
	var req registerReq
	w, _ := run(t, `{"full_name":"A","email":"a@b.co","password":"12345678","password_confirmation":"12345678","age":"x"}`, &req, BindJSON)
	b := decode(t, w)
	if got := b.Errors["age"]; len(got) != 1 || got[0] != "The age field must be an integer." {
		t.Errorf("age = %v", got)
	}
}

func TestBindJSONMalformed(t *testing.T) {
	var req registerReq
	w, _ := run(t, `{"full_name":`, &req, BindJSON)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestValidationErrorManual(t *testing.T) {
	v := NewValidationError().AddRule("email", "unique", nil)
	if v.Message() != "The email has already been taken." {
		t.Errorf("got %q", v.Message())
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"message":"The email has already been taken.","errors":{"email":["The email has already been taken."]}}` {
		t.Errorf("got %s", b)
	}
	if NewValidationError().Message() != MsgInvalidData {
		t.Error("empty bag message")
	}
}

func TestBindQuery(t *testing.T) {
	type q struct {
		Search string `form:"search" binding:"omitempty,min=2"`
		Status string `form:"status" binding:"required"`
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/x?search=a", nil)
	var dst q
	if Bind(c, &dst) {
		t.Fatal("expected failure")
	}
	b := decode(t, w)
	if b.Message != "The search field must be at least 2 characters. (and 1 more error)" {
		t.Errorf("message = %q", b.Message)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{"UserID": "user_id", "FullName": "full_name", "URL": "url", "Role": "role"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}
