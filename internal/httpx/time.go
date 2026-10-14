package httpx

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LaravelTimeLayout is Carbon's default JSON serialisation (toJSON / ISO-8601
// with microseconds, always UTC), e.g. "2025-01-15T08:30:00.000000Z".
const LaravelTimeLayout = "2006-01-02T15:04:05.000000Z"

// DateLayout is the date-only layout.
const DateLayout = "2006-01-02"

// accepted input layouts for Time, in order.
var timeInputLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04",
	DateLayout,
}

// Time is a timestamp that marshals like a Laravel model attribute: UTC with
// microseconds; the zero value marshals as null. It implements sql.Scanner
// and driver.Valuer, so it can be used directly in GORM models (timestamptz).
type Time struct{ time.Time }

// NewTime wraps t.
func NewTime(t time.Time) Time { return Time{t} }

// Now returns the current time.
func Now() Time { return Time{time.Now().UTC()} }

// String renders the Laravel format ("" for zero).
func (t Time) String() string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(LaravelTimeLayout)
}

func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.UTC().Format(LaravelTimeLayout) + `"`), nil
}

func (t *Time) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*t = Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*t = Time{}
		return nil
	}
	for _, l := range timeInputLayouts {
		if v, err := time.Parse(l, s); err == nil {
			*t = Time{v.UTC()}
			return nil
		}
	}
	return fmt.Errorf("httpx.Time: cannot parse %q", s)
}

func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*t = Time{}
	case time.Time:
		*t = Time{v.UTC()}
	default:
		return fmt.Errorf("httpx.Time: cannot scan %T", src)
	}
	return nil
}

func (t Time) Value() (driver.Value, error) {
	if t.IsZero() {
		return nil, nil
	}
	return t.UTC(), nil
}

// GormDataType tells GORM the column type.
func (Time) GormDataType() string { return "timestamptz" }

// Date is a calendar date ("2006-01-02"); zero marshals as null.
// Note: a Laravel `date` cast without a format serialises as a full Time at
// midnight UTC; use Date only where the legacy API used 'date:Y-m-d' or strings.
type Date struct{ time.Time }

// NewDate truncates t to its calendar date (in t's location) at UTC midnight.
func NewDate(t time.Time) Date {
	y, m, d := t.Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ParseDate parses "2006-01-02".
func ParseDate(s string) (Date, error) {
	v, err := time.Parse(DateLayout, strings.TrimSpace(s))
	if err != nil {
		return Date{}, err
	}
	return Date{v}, nil
}

func (d Date) String() string {
	if d.IsZero() {
		return ""
	}
	return d.Format(DateLayout)
}

func (d Date) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + d.Format(DateLayout) + `"`), nil
}

func (d *Date) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = Date{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		*d = Date{}
		return nil
	}
	// Accept full timestamps too and keep the date part.
	if len(s) > len(DateLayout) {
		s = s[:len(DateLayout)]
	}
	v, err := ParseDate(s)
	if err != nil {
		return fmt.Errorf("httpx.Date: cannot parse %q", s)
	}
	*d = v
	return nil
}

func (d *Date) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*d = Date{}
	case time.Time:
		*d = NewDate(v)
	case string:
		p, err := ParseDate(v)
		if err != nil {
			return err
		}
		*d = p
	default:
		return fmt.Errorf("httpx.Date: cannot scan %T", src)
	}
	return nil
}

func (d Date) Value() (driver.Value, error) {
	if d.IsZero() {
		return nil, nil
	}
	return d.Format(DateLayout), nil
}

// GormDataType tells GORM the column type.
func (Date) GormDataType() string { return "date" }
