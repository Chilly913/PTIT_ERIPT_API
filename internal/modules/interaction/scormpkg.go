package interaction

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ScormLimits bound what an uploaded SCORM zip may expand to.
type ScormLimits struct {
	MaxUploadBytes   int64 // the zip itself (Laravel max:204800 KB)
	MaxFiles         int   // entries (files) in the archive
	MaxUnpackedBytes int64 // sum of uncompressed sizes
	MaxManifestBytes int64 // imsmanifest.xml
}

// DefaultScormLimits: 200 MB zip like Laravel, at most 1 GiB / 20k files unpacked.
var DefaultScormLimits = ScormLimits{
	MaxUploadBytes:   204800 * 1024,
	MaxFiles:         20000,
	MaxUnpackedBytes: 1 << 30,
	MaxManifestBytes: 8 << 20,
}

// errScormPackage is a client-side problem with the uploaded package (422).
type errScormPackage struct{ msg string }

func (e *errScormPackage) Error() string { return e.msg }

func packageError(format string, a ...any) error {
	return &errScormPackage{msg: fmt.Sprintf(format, a...)}
}

// scormFile is one regular file of the package.
type scormFile struct {
	Path   string // normalised, relative, forward slashes
	Hidden bool   // a path segment starts with "." (Laravel File::allFiles skips it)
	zf     *zip.File
}

// scormInfo is what extractAndUpload/detectScorm* derived from the package.
type scormInfo struct {
	Entry   string  // launch file, relative to the package root
	Version *string // "1.2" / "2004" or nil
	Title   *string
	Files   []scormFile // in archive order, last duplicate wins
}

// safeZipPath normalises an entry name and rejects anything that could
// escape the package folder (zip-slip): absolute paths, drive letters, "..".
func safeZipPath(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') || strings.ContainsRune(n, 0) {
		return "", packageError("Invalid path in SCORM package: %s", name)
	}
	var segs []string
	for _, s := range strings.Split(n, "/") {
		switch s {
		case "", ".":
			continue
		case "..":
			return "", packageError("Invalid path in SCORM package: %s", name)
		}
		segs = append(segs, s)
	}
	return strings.Join(segs, "/"), nil
}

func isHiddenPath(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, ".") {
			return true
		}
	}
	return false
}

// inspectScorm validates the archive and detects entry, version and title
// exactly like ScormPackageController::detectScorm{Entry,Version,Title}.
func inspectScorm(zr *zip.Reader, lim ScormLimits) (*scormInfo, error) {
	byPath := map[string]int{}
	var files []scormFile
	var total uint64
	for _, f := range zr.File {
		mode := f.Mode()
		if mode.Type()&^fs.ModeDir != 0 { // symlinks, devices, pipes...
			return nil, packageError("Unsupported entry in SCORM package: %s", f.Name)
		}
		p, err := safeZipPath(f.Name)
		if err != nil {
			return nil, err
		}
		if p == "" || mode.IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		total += f.UncompressedSize64
		if lim.MaxUnpackedBytes > 0 && total > uint64(lim.MaxUnpackedBytes) {
			return nil, packageError("SCORM package is too large when unpacked")
		}
		sf := scormFile{Path: p, Hidden: isHiddenPath(p), zf: f}
		if i, ok := byPath[p]; ok {
			files[i] = sf // extractTo overwrites earlier entries
			continue
		}
		byPath[p] = len(files)
		files = append(files, sf)
		if lim.MaxFiles > 0 && len(files) > lim.MaxFiles {
			return nil, packageError("SCORM package contains too many files")
		}
	}

	info := &scormInfo{Files: files}
	var manifest []byte
	if i, ok := byPath["imsmanifest.xml"]; ok {
		raw, err := readZipFile(files[i].zf, lim.MaxManifestBytes)
		if err != nil {
			return nil, err
		}
		manifest = raw
	}
	entry, err := detectScormEntry(manifest, files)
	if err != nil {
		return nil, err
	}
	info.Entry = entry
	if manifest != nil {
		info.Version = detectScormVersion(manifest)
		info.Title = detectScormTitle(manifest)
	}
	return info, nil
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	if limit > 0 && f.UncompressedSize64 > uint64(limit) {
		return nil, packageError("imsmanifest.xml is too large")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, packageError("Cannot open SCORM zip file")
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, packageError("Cannot open SCORM zip file")
	}
	return raw, nil
}

// entryCandidates are probed at the package root when the manifest has no href.
var entryCandidates = []string{"index_lms.html", "index.html", "story.html", "launch.html"}

// detectScormEntry: first non-empty <resources><resource href> of the manifest,
// else a well-known launch file at the root, else the first .html/.htm file.
func detectScormEntry(manifest []byte, files []scormFile) (string, error) {
	if manifest != nil {
		if root := parseXML(manifest); root != nil {
			if res := root.child("resources"); res != nil {
				for _, r := range res.children {
					if r.name != "resource" {
						continue
					}
					if href := r.attr("href"); href != "" {
						return strings.TrimLeft(href, "/"), nil
					}
				}
			}
		}
	}
	exists := map[string]bool{}
	for _, f := range files {
		exists[f.Path] = true
	}
	for _, cnd := range entryCandidates {
		if exists[cnd] {
			return cnd, nil
		}
	}
	// File::allFiles (no dot files), in a deterministic (sorted) order.
	var html []string
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		if !f.Hidden && (ext == ".html" || ext == ".htm") {
			html = append(html, f.Path)
		}
	}
	if len(html) > 0 {
		slices.Sort(html)
		return html[0], nil
	}
	return "", packageError("Cannot detect SCORM entry file")
}

var scorm2004Re = regexp.MustCompile(`adl(cp|seq|nav)_v1p3`)

// detectScormVersion: namespace markers first, then <metadata><schemaversion>.
func detectScormVersion(raw []byte) *string {
	v := func(s string) *string { return &s }
	if bytes.Contains(raw, []byte("adlcp_rootv1p2")) {
		return v("1.2")
	}
	if scorm2004Re.Match(raw) {
		return v("2004")
	}
	root := parseXML(raw)
	if root == nil {
		return nil
	}
	if md := root.child("metadata"); md != nil {
		if sv := md.child("schemaversion"); sv != nil {
			s := sv.text
			switch {
			case s == "":
			case strings.HasPrefix(s, "1.2"):
				return v("1.2")
			case strings.Contains(s, "2004") || strings.Contains(s, "CAM 1.3"):
				return v("2004")
			}
		}
	}
	return nil
}

// detectScormTitle: the first element named "title" (any namespace, document
// order) whose own text is not blank.
func detectScormTitle(raw []byte) *string {
	root := parseXML(raw)
	if root == nil {
		return nil
	}
	var found *string
	root.walk(func(n *xmlNode) bool {
		if n.name == "title" {
			if t := strings.TrimSpace(n.text); t != "" {
				found = &t
				return false
			}
		}
		return true
	})
	return found
}

// ---------------------------------------------------------------------------
// Minimal DOM (SimpleXML-like: element local names, direct text).

type xmlNode struct {
	name     string
	attrs    []xml.Attr
	children []*xmlNode
	text     string // direct character data, like (string) $simplexml
}

func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xmlNode) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// walk visits n and its descendants in document order until fn returns false.
func (n *xmlNode) walk(fn func(*xmlNode) bool) bool {
	if !fn(n) {
		return false
	}
	for _, c := range n.children {
		if !c.walk(fn) {
			return false
		}
	}
	return true
}

// parseXML returns the document element, or nil when the XML is not
// well-formed (simplexml_load_* returning false).
func parseXML(raw []byte) *xmlNode {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	// Manifests declaring another (ASCII-compatible) charset are read as-is.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{name: t.Name.Local, attrs: t.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil // second document element
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if len(stack) != 0 {
		return nil
	}
	return root
}

// resolveScormURL is ScormPackageController::resolvePublicUrl: stored object
// keys are served through the nginx /minio/<bucket>/ proxy; absolute URLs
// (H5P embeds) and /minio/ paths pass through.
func resolveScormURL(keyOrURL, bucket string) string {
	if keyOrURL == "" {
		return ""
	}
	if strings.HasPrefix(keyOrURL, "http") || strings.HasPrefix(keyOrURL, "/minio/") {
		return keyOrURL
	}
	return "/minio/" + bucket + "/" + strings.TrimLeft(keyOrURL, "/")
}

// normalizeScormVersion maps a client-supplied version (Laravel accepted any
// string up to 50 chars) onto the values the schema allows.
func normalizeScormVersion(v string) (string, bool) {
	switch {
	case v == "1.2" || v == "2004" || v == "h5p":
		return v, true
	case strings.EqualFold(v, "h5p"):
		return "h5p", true
	case strings.HasPrefix(v, "1.2"):
		return "1.2", true
	case strings.Contains(v, "2004") || strings.Contains(v, "CAM 1.3"):
		return "2004", true
	}
	return "", false
}
