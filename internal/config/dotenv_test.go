package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	t.Setenv("DOTENV_KEEP", "from-env")
	path := filepath.Join(t.TempDir(), ".env")
	body := "\ufeff# comment\nDOTENV_A=1\nexport DOTENV_B=\"two words\"\nDOTENV_C='x'\nDOTENV_D=val # trailing\nDOTENV_KEEP=from-file\nbad line\n\nDOTENV_EMPTY=\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"DOTENV_A", "DOTENV_B", "DOTENV_C", "DOTENV_D", "DOTENV_EMPTY"} {
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	n, err := LoadDotEnv(path)
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	want := map[string]string{"DOTENV_A": "1", "DOTENV_B": "two words", "DOTENV_C": "x", "DOTENV_D": "val", "DOTENV_KEEP": "from-env", "DOTENV_EMPTY": ""}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if n, err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); n != 0 || err != nil {
		t.Fatalf("missing file: n=%d err=%v", n, err)
	}
}
