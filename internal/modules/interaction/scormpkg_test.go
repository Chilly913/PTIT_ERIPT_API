package interaction

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	body string
	mode fs.FileMode
}

func buildZip(t *testing.T, entries ...zipEntry) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

func wantPackageError(t *testing.T, err error, contains string) {
	t.Helper()
	var pe *errScormPackage
	if !errors.As(err, &pe) {
		t.Fatalf("want package error %q, got %v", contains, err)
	}
	if !strings.Contains(pe.msg, contains) {
		t.Fatalf("message %q does not contain %q", pe.msg, contains)
	}
}

func TestSafeZipPath(t *testing.T) {
	ok := map[string]string{
		"index.html":           "index.html",
		"a/b/c.js":             "a/b/c.js",
		"./a//b/./c.js":        "a/b/c.js",
		`win\style\main.css`:   "win/style/main.css",
		"dir/":                 "dir",
		"a/..b/c":              "a/..b/c",
		"shared/.hidden/x.txt": "shared/.hidden/x.txt",
	}
	for in, want := range ok {
		got, err := safeZipPath(in)
		if err != nil || got != want {
			t.Errorf("safeZipPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"../evil.html", "a/../../evil", "/etc/passwd", `..\..\evil.bat`, `C:\Windows\x.dll`, "c:/x", "a/\x00b"} {
		if _, err := safeZipPath(bad); err == nil {
			t.Errorf("safeZipPath(%q) accepted", bad)
		}
	}
}

const manifest12 = `<?xml version="1.0" encoding="UTF-8"?>
<manifest identifier="m1" version="1.0"
  xmlns="http://www.imsproject.org/xsd/imscp_rootv1p1p2"
  xmlns:adlcp="http://www.adlnet.org/xsd/adlcp_rootv1p2">
  <metadata><schema>ADL SCORM</schema><schemaversion>1.2</schemaversion></metadata>
  <organizations default="o1">
    <organization identifier="o1">
      <title>  Intro to Go  </title>
      <item identifier="i1" identifierref="r1"><title>Lesson 1</title></item>
    </organization>
  </organizations>
  <resources>
    <resource identifier="r0" type="webcontent" adlcp:scormtype="asset"/>
    <resource identifier="r1" type="webcontent" adlcp:scormtype="sco" href="/content/start.html"/>
  </resources>
</manifest>`

func TestInspectScormManifest(t *testing.T) {
	zr := buildZip(t,
		zipEntry{name: "imsmanifest.xml", body: manifest12},
		zipEntry{name: "content/"},
		zipEntry{name: "content/start.html", body: "<html></html>"},
		zipEntry{name: ".DS_Store", body: "x"},
		zipEntry{name: "content/.git/config", body: "x"},
	)
	info, err := inspectScorm(zr, DefaultScormLimits)
	if err != nil {
		t.Fatal(err)
	}
	if info.Entry != "content/start.html" {
		t.Fatalf("entry %q", info.Entry)
	}
	if info.Version == nil || *info.Version != "1.2" {
		t.Fatalf("version %v", info.Version)
	}
	if info.Title == nil || *info.Title != "Intro to Go" {
		t.Fatalf("title %v", info.Title)
	}
	var visible []string
	for _, f := range info.Files {
		if !f.Hidden {
			visible = append(visible, f.Path)
		}
	}
	if strings.Join(visible, ",") != "imsmanifest.xml,content/start.html" {
		t.Fatalf("visible files %v", visible)
	}
}

func TestInspectScormFallbacks(t *testing.T) {
	// No manifest: well-known root candidate in candidate order.
	info, err := inspectScorm(buildZip(t,
		zipEntry{name: "story.html", body: "s"},
		zipEntry{name: "index.html", body: "i"},
	), DefaultScormLimits)
	if err != nil || info.Entry != "index.html" || info.Version != nil || info.Title != nil {
		t.Fatalf("candidates: %+v %v", info, err)
	}
	// Then the first html file (sorted), ignoring dot files.
	info, err = inspectScorm(buildZip(t,
		zipEntry{name: "pkg/b/page.HTM", body: "b"},
		zipEntry{name: ".hidden/a.html", body: "a"},
		zipEntry{name: "pkg/a/page.html", body: "a"},
	), DefaultScormLimits)
	if err != nil || info.Entry != "pkg/a/page.html" {
		t.Fatalf("first html: %+v %v", info, err)
	}
	// Manifest without href and malformed XML fall back too.
	info, err = inspectScorm(buildZip(t,
		zipEntry{name: "imsmanifest.xml", body: `<manifest><resources><resource href=""/></resources>`},
		zipEntry{name: "launch.html", body: "l"},
	), DefaultScormLimits)
	if err != nil || info.Entry != "launch.html" {
		t.Fatalf("malformed manifest: %+v %v", info, err)
	}
	// Nothing launchable.
	_, err = inspectScorm(buildZip(t, zipEntry{name: "a.txt", body: "x"}), DefaultScormLimits)
	wantPackageError(t, err, "Cannot detect SCORM entry file")
}

func TestInspectScormRejectsUnsafeArchives(t *testing.T) {
	_, err := inspectScorm(buildZip(t,
		zipEntry{name: "index.html", body: "ok"},
		zipEntry{name: "../../etc/cron.d/evil", body: "x"},
	), DefaultScormLimits)
	wantPackageError(t, err, "Invalid path")

	_, err = inspectScorm(buildZip(t,
		zipEntry{name: "index.html", body: "ok"},
		zipEntry{name: "link", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
	), DefaultScormLimits)
	wantPackageError(t, err, "Unsupported entry")

	lim := DefaultScormLimits
	lim.MaxFiles = 2
	_, err = inspectScorm(buildZip(t,
		zipEntry{name: "index.html", body: "1"},
		zipEntry{name: "b.js", body: "2"},
		zipEntry{name: "c.js", body: "3"},
	), lim)
	wantPackageError(t, err, "too many files")

	lim = DefaultScormLimits
	lim.MaxUnpackedBytes = 1000
	_, err = inspectScorm(buildZip(t,
		zipEntry{name: "index.html", body: strings.Repeat("a", 600)},
		zipEntry{name: "big.js", body: strings.Repeat("b", 600)},
	), lim)
	wantPackageError(t, err, "too large")

	lim = DefaultScormLimits
	lim.MaxManifestBytes = 10
	_, err = inspectScorm(buildZip(t, zipEntry{name: "imsmanifest.xml", body: manifest12}), lim)
	wantPackageError(t, err, "imsmanifest.xml is too large")
}

func TestInspectScormDuplicateEntriesLastWins(t *testing.T) {
	info, err := inspectScorm(buildZip(t,
		zipEntry{name: "index.html", body: "old"},
		zipEntry{name: "./index.html", body: "new"},
	), DefaultScormLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Files) != 1 || info.Files[0].zf.UncompressedSize64 != 3 || info.Files[0].zf.Name != "./index.html" {
		t.Fatalf("files %+v", info.Files)
	}
}

func TestDetectScormVersion(t *testing.T) {
	cases := map[string]string{
		manifest12: "1.2",
		`<manifest xmlns:adlcp="http://www.adlnet.org/xsd/adlcp_v1p3"/>`:                            "2004",
		`<manifest xmlns:adlseq="http://www.adlnet.org/xsd/adlseq_v1p3"/>`:                          "2004",
		`<manifest><metadata><schemaversion>2004 4th Edition</schemaversion></metadata></manifest>`: "2004",
		`<manifest><metadata><schemaversion>CAM 1.3</schemaversion></metadata></manifest>`:          "2004",
		`<manifest><metadata><schemaversion>1.2.1</schemaversion></metadata></manifest>`:            "1.2",
		`<manifest><metadata><schemaversion>9</schemaversion></metadata></manifest>`:                "",
		`<manifest><metadata>`: "",
	}
	for raw, want := range cases {
		got := detectScormVersion([]byte(raw))
		if (got == nil && want != "") || (got != nil && *got != want) {
			t.Errorf("detectScormVersion(%.60q) = %v, want %q", raw, got, want)
		}
	}
}

func TestDetectScormTitle(t *testing.T) {
	raw := `<manifest xmlns:lom="http://ltsc.ieee.org/xsd/LOM">
  <metadata><lom:lom><lom:general><lom:title><lom:string>LOM title</lom:string></lom:title></lom:general></lom:lom></metadata>
  <organizations><organization><title>   </title><title>Org title</title></organization></organizations>
</manifest>`
	got := detectScormTitle([]byte(raw))
	if got == nil || *got != "Org title" {
		t.Fatalf("title %v", got)
	}
	if detectScormTitle([]byte(`<manifest/>`)) != nil || detectScormTitle([]byte(`not xml`)) != nil {
		t.Fatal("expected nil title")
	}
}

func TestResolveScormURL(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"scorm/u1/index.html":            "/minio/lms-videos/scorm/u1/index.html",
		"/scorm/u1/index.html":           "/minio/lms-videos/scorm/u1/index.html",
		"https://h5p.org/h5p/embed/1":    "https://h5p.org/h5p/embed/1",
		"/minio/lms-videos/scorm/x.html": "/minio/lms-videos/scorm/x.html",
	}
	for in, want := range cases {
		if got := resolveScormURL(in, "lms-videos"); got != want {
			t.Errorf("resolveScormURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeScormVersion(t *testing.T) {
	cases := map[string]string{"1.2": "1.2", "2004": "2004", "h5p": "h5p", "H5P": "h5p", "1.2.3": "1.2",
		"SCORM 2004 3rd": "2004", "CAM 1.3": "2004", "1.1": "", "": ""}
	for in, want := range cases {
		got, ok := normalizeScormVersion(in)
		if ok != (want != "") || got != want {
			t.Errorf("normalizeScormVersion(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
