package content

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

const maxUploadBytes = 5 << 20 // 5 MB

// allowedImages maps a sniffed content type to the file extension we store it under.
// SVG is intentionally excluded: user-supplied SVG can carry scripts.
var allowedImages = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// uploadDir is where uploaded files are stored (UPLOAD_DIR, default ./uploads).
func uploadDir() string {
	if d := strings.TrimSpace(os.Getenv("UPLOAD_DIR")); d != "" {
		return d
	}
	return "uploads"
}

// adminUpload stores one image (multipart field "file") and returns its public URL.
func (m *Module) adminUpload(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file field is required."))
		return
	}
	if fh.Size > maxUploadBytes {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must not be greater than 5120 kilobytes."))
		return
	}
	src, err := fh.Open()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer src.Close()

	head := make([]byte, 512)
	n, _ := io.ReadFull(src, head)
	ext, ok := allowedImages[http.DetectContentType(head[:n])]
	if !ok {
		httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must be an image (png, jpg, gif or webp)."))
		return
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		httpx.Fail(c, err)
		return
	}

	sub := time.Now().UTC().Format("200601")
	dir := filepath.Join(uploadDir(), sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpx.Fail(c, err)
		return
	}
	var rnd [12]byte
	_, _ = rand.Read(rnd[:])
	name := hex.EncodeToString(rnd[:]) + ext
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"url": fmt.Sprintf("/uploads/%s/%s", sub, name), "name": fh.Filename, "size": fh.Size})
}

// serveUpload serves a stored file publicly (immutable: names are random).
func (m *Module) serveUpload(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.FileFromFS(c.Param("path"), http.Dir(uploadDir()))
}
