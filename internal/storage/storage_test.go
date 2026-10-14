package storage

import (
	"testing"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
)

func TestPublicBaseURL(t *testing.T) {
	cases := []struct {
		cfg  config.StorageConfig
		want string
	}{
		{config.StorageConfig{Endpoint: "localhost:9000", Bucket: "lms", PathStyle: true}, "http://localhost:9000/lms"},
		{config.StorageConfig{Endpoint: "s3.amazonaws.com", Bucket: "lms", UseSSL: true}, "https://lms.s3.amazonaws.com"},
		{config.StorageConfig{PublicURL: "https://eript-lms.io.vn/minio/lms/", Bucket: "lms"}, "https://eript-lms.io.vn/minio/lms"},
	}
	for _, tc := range cases {
		if got := PublicBaseURL(tc.cfg); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
}

func TestJoinURL(t *testing.T) {
	base := "https://cdn.example/lms"
	cases := map[string]string{
		"":                            "",
		"avatars/1/a b.png":           "https://cdn.example/lms/avatars/1/a%20b.png",
		"/courses\\x.jpg":             "https://cdn.example/lms/courses/x.jpg",
		"https://other.example/x.png": "https://other.example/x.png",
	}
	for key, want := range cases {
		if got := JoinURL(base, key); got != want {
			t.Errorf("JoinURL(%q) = %q want %q", key, got, want)
		}
	}
}

func TestNewDoesNotDial(t *testing.T) {
	s, err := New(config.StorageConfig{Endpoint: "localhost:1", Bucket: "b", PathStyle: true, AccessKey: "a", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if s.URL("x/y.mp4") != "http://localhost:1/b/x/y.mp4" {
		t.Fatalf("got %s", s.URL("x/y.mp4"))
	}
}
