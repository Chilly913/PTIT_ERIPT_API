package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type envSource interface {
	Lookup(key string) (string, bool)
}

type osEnv struct{}

func (osEnv) Lookup(key string) (string, bool) { return os.LookupEnv(key) }

type mapEnv map[string]string

func (m mapEnv) Lookup(key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}

// env reads typed values and accumulates parse errors.
type env struct {
	src  envSource
	errs []error
}

// raw returns the trimmed value; empty and the Laravel-style "null" count as unset.
func (e *env) raw(key string) (string, bool) {
	v, ok := e.src.Lookup(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" || v == "null" {
		return "", false
	}
	return v, true
}

func (e *env) str(key, def string) string {
	if v, ok := e.raw(key); ok {
		return v
	}
	return def
}

func (e *env) int(key string, def int) int {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

func (e *env) bool(key string, def bool) bool {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(strings.ToLower(v))
	if err != nil {
		switch strings.ToLower(v) {
		case "yes", "on":
			return true
		case "no", "off":
			return false
		}
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a boolean", key, v))
		return def
	}
	return b
}

// duration accepts Go durations ("30s", "5m") or a bare integer of seconds.
func (e *env) duration(key string, def time.Duration) time.Duration {
	v, ok := e.raw(key)
	if !ok {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a duration", key, v))
		return def
	}
	return d
}

// list splits a comma-separated value, dropping blanks.
func (e *env) list(key, def string) []string {
	v := e.str(key, def)
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
