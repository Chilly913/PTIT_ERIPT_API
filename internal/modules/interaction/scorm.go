package interaction

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// scormUploadWorkers is the number of parallel object uploads per package.
const scormUploadWorkers = 8

func (m *Module) findScormPackage(ctx context.Context, db *gorm.DB, lessonID int64) (*scormPackageRow, error) {
	var rows []scormPackageRow
	if err := db.WithContext(ctx).Raw(`SELECT `+scormPackageCols+` FROM scorm_packages WHERE lesson_id = ?`, lessonID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// showScormPackage is ScormPackageController@show (any authenticated user).
func (m *Module) showScormPackage(c *gin.Context) {
	lesson := lessonOf(c)
	if lesson.CourseID != courseOf(c).ID {
		notFound(c)
		return
	}
	pkg, err := m.findScormPackage(c.Request.Context(), m.db, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if pkg == nil {
		httpx.Abort(c, http.StatusNotFound, "No package found")
		return
	}
	httpx.OK(c, m.scormPackageJSON(*pkg))
}

// storeScormPackage is ScormPackageController@store: a SCORM zip (extracted
// to scorm/<uuid>/ in object storage) or an H5P embed URL, upserted per lesson.
func (m *Module) storeScormPackage(c *gin.Context) {
	ctx := c.Request.Context()
	who, ok := m.caller(c)
	if !ok {
		return
	}
	course, lesson := courseOf(c), lessonOf(c)
	if !who.IsOwner(course) {
		forbidden(c)
		return
	}
	if lesson.CourseID != course.ID {
		notFound(c)
		return
	}

	// Bound the body: the zip limit plus room for the other fields, so a file
	// slightly over the limit still gets Laravel's 422 "max" message.
	if m.limits.MaxUploadBytes > 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, m.limits.MaxUploadBytes+8<<20)
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	maxKB := m.limits.MaxUploadBytes / 1024
	if err := validate(in, nil,
		rules("entry_url", "nullable|url|max:2048"),
		rules("title", "nullable|string|max:255"),
		rules("identifier", "nullable|string|max:255"),
		rules("version", "nullable|string|max:50"),
		rules("type", "required|string|in:scorm,h5p"),
		rules("scorm_file", fmt.Sprintf("nullable|file|mimes:zip|max:%d", maxKB)),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	typ := *in.str("type")
	file := in.file("scorm_file")
	entryURL := in.str("entry_url")

	if typ == "scorm" && file == nil {
		httpx.Abort(c, http.StatusUnprocessableEntity, "SCORM package file is required")
		return
	}
	if typ == "h5p" && entryURL == nil {
		httpx.Abort(c, http.StatusUnprocessableEntity, "H5P embed URL is required")
		return
	}
	// Deviation: the schema only allows 1.2 / 2004 / h5p.
	var version *string
	if v := in.str("version"); v != nil {
		nv, ok := normalizeScormVersion(*v)
		if !ok {
			httpx.Fail(c, httpx.NewValidationError().AddRule("version", "in", nil))
			return
		}
		version = &nv
	}

	existing, err := m.findScormPackage(ctx, m.db, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	pkgUUID := uuid.NewString()
	if existing != nil {
		pkgUUID = existing.UUID
	}

	var detectedVersion, detectedTitle *string
	if typ == "scorm" {
		if m.store == nil {
			httpx.Fail(c, errors.New("interaction: object storage is not configured"))
			return
		}
		info, closeZip, err := m.openScorm(file)
		if err != nil {
			m.failScorm(c, err)
			return
		}
		defer closeZip()
		prefix := "scorm/" + pkgUUID
		// Laravel deleted the old objects before extracting; Go first checks
		// that the new package is usable so a bad upload keeps the old one.
		if existing != nil {
			if err := m.store.DeletePrefix(ctx, prefix+"/"); err != nil {
				httpx.Fail(c, err)
				return
			}
		}
		if err := m.uploadScorm(ctx, prefix, info.Files); err != nil {
			if derr := m.store.DeletePrefix(context.WithoutCancel(ctx), prefix+"/"); derr != nil {
				m.log.ErrorContext(ctx, "scorm cleanup failed", "prefix", prefix, "error", derr.Error())
			}
			m.failScorm(c, err)
			return
		}
		entryKey := prefix + "/" + info.Entry
		entryURL = &entryKey
		detectedVersion, detectedTitle = info.Version, info.Title
	}

	if version == nil {
		version = detectedVersion
	}
	if version == nil {
		v := "1.2"
		if typ == "h5p" {
			v = "h5p"
		}
		version = &v
	}
	title := in.str("title")
	if title == nil {
		title = detectedTitle
	}
	if title == nil {
		title = &lesson.Title
	}
	if title != nil && len([]rune(*title)) > 255 { // manifest titles are not validated
		t := string([]rune(*title)[:255])
		title = &t
	}

	var pkg scormPackageRow
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []scormPackageRow
		if err := tx.Raw(`INSERT INTO scorm_packages (lesson_id, uuid, version, entry_path, identifier, title)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (lesson_id) DO UPDATE SET
  uuid = EXCLUDED.uuid, version = EXCLUDED.version, entry_path = EXCLUDED.entry_path,
  identifier = EXCLUDED.identifier, title = EXCLUDED.title
RETURNING `+scormPackageCols,
			lesson.ID, pkgUUID, *version, *entryURL, in.str("identifier"), title).Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.New("interaction: upsert scorm_packages returned no row")
		}
		pkg = rows[0]
		return tx.Exec(`UPDATE lessons SET type = ? WHERE id = ? AND type <> ?`, typ, lesson.ID, typ).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{
		"message":       "SCORM/H5P package saved successfully",
		"scorm_package": m.scormPackageJSON(pkg),
	})
}

// openScorm opens the uploaded zip and inspects it. The returned func closes
// the upload, which must stay open while the files are uploaded.
func (m *Module) openScorm(fh *multipart.FileHeader) (*scormInfo, func(), error) {
	f, err := fh.Open()
	if err != nil {
		return nil, nil, err
	}
	zr, err := zip.NewReader(f, fh.Size)
	if err != nil {
		_ = f.Close()
		return nil, nil, packageError("Cannot open SCORM zip file")
	}
	info, err := inspectScorm(zr, m.limits)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return info, func() { _ = f.Close() }, nil
}

// uploadScorm puts every non-hidden file under prefix (Laravel uploaded
// File::allFiles, which skips dot files).
func (m *Module) uploadScorm(ctx context.Context, prefix string, files []scormFile) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan scormFile)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}
	for range scormUploadWorkers {
		wg.Go(func() {
			for f := range jobs {
				if err := m.putZipFile(ctx, prefix+"/"+f.Path, f); err != nil {
					fail(err)
				}
			}
		})
	}
loop:
	for _, f := range files {
		if f.Hidden {
			continue
		}
		select {
		case jobs <- f:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func (m *Module) putZipFile(ctx context.Context, key string, f scormFile) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	rc, err := f.zf.Open()
	if err != nil {
		return packageError("Cannot open SCORM zip file")
	}
	defer rc.Close()
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(f.Path)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if err := m.store.Put(ctx, key, rc, int64(f.zf.UncompressedSize64), ct); err != nil {
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrAlgorithm) {
			return packageError("Cannot open SCORM zip file")
		}
		return err
	}
	return nil
}

// failScorm renders package problems as 422 (Laravel: 500 RuntimeException).
func (m *Module) failScorm(c *gin.Context, err error) {
	var pe *errScormPackage
	if errors.As(err, &pe) {
		httpx.Abort(c, http.StatusUnprocessableEntity, pe.msg)
		return
	}
	httpx.Fail(c, err)
}

// destroyScormPackage is ScormPackageController@destroy. Deviation: the
// lesson must belong to {course} (Laravel skipped the check, letting any
// course owner delete another course's package); 404 "Not found" otherwise.
func (m *Module) destroyScormPackage(c *gin.Context) {
	ctx := c.Request.Context()
	who, ok := m.caller(c)
	if !ok {
		return
	}
	course, lesson := courseOf(c), lessonOf(c)
	if !who.IsOwner(course) {
		forbidden(c)
		return
	}
	if lesson.CourseID != course.ID {
		notFound(c)
		return
	}
	pkg, err := m.findScormPackage(ctx, m.db, lesson.ID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if pkg != nil && pkg.UUID != "" && m.store != nil {
		if err := m.store.DeletePrefix(ctx, "scorm/"+pkg.UUID+"/"); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	if err := m.db.WithContext(ctx).Exec(`DELETE FROM scorm_packages WHERE lesson_id = ?`, lesson.ID).Error; err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "SCORM/H5P package removed"})
}

// scormCompleted is ScormController's completion rule: SCORM 1.2 counts
// completed/passed, 2004 counts completion=completed or success=passed.
func scormCompleted(version string, status, completion, success *string) bool {
	eq := func(p *string, v string) bool { return p != nil && *p == v }
	if version == "2004" {
		return eq(completion, "completed") || eq(success, "passed")
	}
	return eq(status, "completed") || eq(status, "passed")
}

// trackScorm is ScormController@track. Like Laravel there is no enrollment
// check. The runtime blob (legacy lesson_progress.metadata) now lives in
// scorm_runtime_states; lesson_progress.completed_at is set to now() when
// completed and cleared otherwise (legacy updateOrCreate semantics).
//
// NOTE for the lead: this writes lesson_progress directly (owned by the study
// module) — dedupe with study's progress upsert later.
func (m *Module) trackScorm(c *gin.Context) {
	ctx := c.Request.Context()
	lesson := lessonOf(c)
	if lesson.CourseID != courseOf(c).ID {
		notFound(c)
		return
	}
	in, ok := bindInput(c)
	if !ok {
		return
	}
	if err := validate(in, nil,
		rules("version", "nullable|string|in:1.2,2004"),
		rules("status", "nullable|string|in:completed,passed,failed,browsed,incomplete,unknown"),
		rules("completion_status", "nullable|string|in:completed,incomplete,not_attempted,unknown"),
		rules("success_status", "nullable|string|in:passed,failed,unknown"),
		rules("score", "nullable|numeric|min:0|max:100"),
		rules("lesson_location", "nullable|string"),
		rules("suspend_data", "nullable|string"),
	); err != nil {
		httpx.Fail(c, err)
		return
	}
	version := "1.2"
	if v := in.str("version"); v != nil {
		version = *v
	}
	status, completion, success := in.str("status"), in.str("completion_status"), in.str("success_status")
	completed := scormCompleted(version, status, completion, success)
	var score *float64
	if f, ok := in.float("score"); ok {
		score = &f
	}
	var completedAt *time.Time
	if completed {
		now := time.Now().UTC()
		completedAt = &now
	}
	uid := auth.UserID(c)

	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO lesson_progress (user_id, lesson_id, completed_at) VALUES (?, ?, ?)
ON CONFLICT (user_id, lesson_id) DO UPDATE SET completed_at = EXCLUDED.completed_at`,
			uid, lesson.ID, completedAt).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO scorm_runtime_states
  (user_id, lesson_id, scorm_version, lesson_status, completion_status, success_status, score, lesson_location, suspend_data)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (user_id, lesson_id) DO UPDATE SET
  scorm_version = EXCLUDED.scorm_version, lesson_status = EXCLUDED.lesson_status,
  completion_status = EXCLUDED.completion_status, success_status = EXCLUDED.success_status,
  score = EXCLUDED.score, lesson_location = EXCLUDED.lesson_location, suspend_data = EXCLUDED.suspend_data`,
			uid, lesson.ID, version, status, completion, success, score,
			in.str("lesson_location"), in.str("suspend_data")).Error
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"message": "Progress tracked", "completed": completed})
}
