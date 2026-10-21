// Package crud is a small declarative CRUD toolkit for admin tables: paginated list with search,
// sort and filters, create/update/delete, CSV export and CSV import.
package crud

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// FK describes a foreign key: the referenced table and the column used to look it up in CSV imports.
type FK struct{ Table, By string }

// Field is one writable column of a resource.
type Field struct {
	Key      string // JSON key (request/response) and query parameter
	Col      string // database column
	Kind     string // text | int | bool | date | enum | fk
	Required bool
	Max      int // max chars for text, max value for int (0 = default)
	Min      int // min value for int
	Enum     []string
	FK       *FK
	Import   string // CSV header; defaults to Key
	DefBool  bool   // value used when a bool is missing from a create request
	Default  any    // value used when an optional field is left blank (for NOT NULL columns)
}

// Filter maps a query parameter to a SQL condition (the value is bound to the single "?").
type Filter struct {
	Param string
	Expr  string
	Bool  bool
}

// ExportCol is one CSV column (Key is a key of the list row).
type ExportCol struct{ Header, Key string }

// Resource declares a simple CRUD table; see resources.go.
type Resource struct {
	Path        string
	Table       string
	File        string // download file name without extension
	Fields      []Field
	Select      string // select list for rows (table alias is t)
	From        string // FROM clause with joins
	Search      []string
	Sort        map[string]string
	DefaultSort string
	Filters     []Filter
	Export      []ExportCol
	UniqueKey   string // field reported when a unique constraint fails
	// Check validates cross-field rules and returns field -> message.
	Check func(v map[string]any) map[string]string
	// Before runs inside the write transaction (e.g. to clear a previous "current" flag).
	Before func(tx *gorm.DB, id int64, v map[string]any) error
	// Extra returns columns set on insert only (e.g. institution_id).
	Extra func(tx *gorm.DB) (map[string]any, error)
}

func (f Field) header() string {
	if f.Import != "" {
		return f.Import
	}
	return f.Key
}

// pgState returns the PostgreSQL SQLSTATE of err ("" when it is not a database error).
func pgState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func isDuplicate(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || pgState(err) == "23505"
}
func isFKViolation(err error) bool {
	st := pgState(err)
	return errors.Is(err, gorm.ErrForeignKeyViolated) || st == "23503" || st == "23001"
}

// ---- parsing ------------------------------------------------------------

func asString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func (r *Resource) parse(tx *gorm.DB, body map[string]any) (map[string]any, *httpx.ValidationError) {
	bag := httpx.NewValidationError()
	out := map[string]any{}
	for _, f := range r.Fields {
		raw, present := body[f.Key]
		s, isStr := asString(raw)
		blank := !present || raw == nil || (isStr && s == "")
		if f.Kind == "bool" {
			switch b := raw.(type) {
			case bool:
				out[f.Key] = b
			case string:
				out[f.Key] = b == "true" || b == "1"
			default:
				out[f.Key] = f.DefBool
			}
			continue
		}
		if blank {
			if f.Required {
				bag.Add(f.Key, fmt.Sprintf("The %s field is required.", strings.ReplaceAll(f.Key, "_", " ")))
			}
			out[f.Key] = f.Default
			continue
		}
		switch f.Kind {
		case "text":
			max := f.Max
			if max == 0 {
				max = 255
			}
			if len([]rune(s)) > max {
				bag.Add(f.Key, fmt.Sprintf("The %s must not be greater than %d characters.", strings.ReplaceAll(f.Key, "_", " "), max))
				continue
			}
			out[f.Key] = s
		case "int", "fk":
			n, err := strconv.ParseFloat(s, 64)
			if err != nil || n != math.Trunc(n) {
				bag.Add(f.Key, fmt.Sprintf("The %s must be an integer.", strings.ReplaceAll(f.Key, "_", " ")))
				continue
			}
			if f.Kind == "int" {
				if int(n) < f.Min || (f.Max != 0 && int(n) > f.Max) {
					bag.Add(f.Key, fmt.Sprintf("The %s is out of range.", strings.ReplaceAll(f.Key, "_", " ")))
					continue
				}
				out[f.Key] = int64(n)
				continue
			}
			var cnt int64
			if err := tx.Table(f.FK.Table).Where("id = ?", int64(n)).Count(&cnt).Error; err != nil {
				bag.Add(f.Key, "The selected "+f.Key+" is invalid.")
				continue
			}
			if cnt == 0 {
				bag.Add(f.Key, "The selected "+strings.ReplaceAll(f.Key, "_", " ")+" is invalid.")
				continue
			}
			out[f.Key] = int64(n)
		case "date":
			if _, err := time.Parse("2006-01-02", s); err != nil {
				bag.Add(f.Key, fmt.Sprintf("The %s is not a valid date.", strings.ReplaceAll(f.Key, "_", " ")))
				continue
			}
			out[f.Key] = s
		case "enum":
			ok := false
			for _, e := range f.Enum {
				if e == s {
					ok = true
				}
			}
			if !ok {
				bag.Add(f.Key, fmt.Sprintf("The selected %s is invalid.", strings.ReplaceAll(f.Key, "_", " ")))
				continue
			}
			out[f.Key] = s
		}
	}
	if !bag.HasErrors() && r.Check != nil {
		for k, msg := range r.Check(out) {
			bag.Add(k, msg)
		}
	}
	if bag.HasErrors() {
		return nil, bag
	}
	return out, nil
}

// ---- list ---------------------------------------------------------------

type listQuery struct {
	where string
	args  []any
	order string
}

func (r *Resource) query(c *gin.Context) listQuery {
	var conds []string
	var args []any
	if s := strings.TrimSpace(c.Query("search")); s != "" && len(r.Search) > 0 {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s) + "%"
		var parts []string
		for _, col := range r.Search {
			parts = append(parts, col+"::text ILIKE ?")
			args = append(args, like)
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	for _, f := range r.Filters {
		v := strings.TrimSpace(c.Query(f.Param))
		if v == "" {
			continue
		}
		conds = append(conds, f.Expr)
		if f.Bool {
			args = append(args, v == "true" || v == "1")
		} else {
			args = append(args, v)
		}
	}
	key := c.Query("sort")
	expr, ok := r.Sort[key]
	if !ok {
		key = r.DefaultSort
		expr = r.Sort[key]
	}
	dir := "ASC"
	if strings.EqualFold(c.Query("dir"), "desc") {
		dir = "DESC"
	}
	q := listQuery{args: args, order: expr + " " + dir + ", t.id DESC"}
	if len(conds) > 0 {
		q.where = " WHERE " + strings.Join(conds, " AND ")
	}
	return q
}

func (r *Resource) rows(db *gorm.DB, q listQuery, limit, offset int) ([]map[string]any, error) {
	sql := "SELECT " + r.Select + " FROM " + r.From + q.where + " ORDER BY " + q.order
	args := append([]any{}, q.args...)
	if limit > 0 {
		sql += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	out := []map[string]any{}
	if err := db.Raw(sql, args...).Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Resource) one(db *gorm.DB, id int64) (map[string]any, error) {
	rows := []map[string]any{}
	if err := db.Raw("SELECT "+r.Select+" FROM "+r.From+" WHERE t.id = ?", id).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return rows[0], nil
}

func (r *Resource) list(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := httpx.ParsePage(c, 10, 100)
		q := r.query(c)
		var total int64
		if err := db.Raw("SELECT count(*) FROM "+r.From+q.where, q.args...).Scan(&total).Error; err != nil {
			httpx.Fail(c, err)
			return
		}
		rows, err := r.rows(db, q, p.PerPage, (p.Page-1)*p.PerPage)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		last := int((total + int64(p.PerPage) - 1) / int64(p.PerPage))
		if last < 1 {
			last = 1
		}
		c.JSON(http.StatusOK, gin.H{"data": rows, "meta": gin.H{"page": p.Page, "per_page": p.PerPage, "total": total, "last_page": last}})
	}
}

// ---- write --------------------------------------------------------------

func (r *Resource) save(db *gorm.DB, editing bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var id int64
		if editing {
			var err error
			if id, err = strconv.ParseInt(c.Param("id"), 10, 64); err != nil || id < 1 {
				httpx.NotFound(c, "")
				return
			}
		}
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.Abort(c, http.StatusBadRequest, "The request body is not valid JSON.")
			return
		}
		saved, err := r.write(db, id, body)
		if err != nil {
			r.fail(c, err)
			return
		}
		status := http.StatusOK
		if !editing {
			status = http.StatusCreated
		}
		c.JSON(status, saved)
	}
}

// write validates body and inserts (id == 0) or updates the row, returning the fresh list row.
func (r *Resource) write(db *gorm.DB, id int64, body map[string]any) (map[string]any, error) {
	var newID int64
	err := db.Transaction(func(tx *gorm.DB) error {
		vals, verr := r.parse(tx, body)
		if verr != nil {
			return verr
		}
		if r.Before != nil {
			if err := r.Before(tx, id, vals); err != nil {
				return err
			}
		}
		cols := make([]string, 0, len(r.Fields))
		args := make([]any, 0, len(r.Fields))
		for _, f := range r.Fields {
			cols = append(cols, f.Col)
			args = append(args, vals[f.Key])
		}
		if id == 0 {
			if r.Extra != nil {
				extra, err := r.Extra(tx)
				if err != nil {
					return err
				}
				for col, v := range extra {
					cols = append(cols, col)
					args = append(args, v)
				}
			}
			ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
			return tx.Raw("INSERT INTO "+r.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+ph+") RETURNING id", args...).Scan(&newID).Error
		}
		sets := make([]string, len(cols))
		for i, col := range cols {
			sets[i] = col + " = ?"
		}
		args = append(args, id)
		res := tx.Raw("UPDATE "+r.Table+" SET "+strings.Join(sets, ", ")+" WHERE id = ? RETURNING id", args...).Scan(&newID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.one(db, newID)
}

func (r *Resource) fail(c *gin.Context, err error) {
	switch {
	case isDuplicate(err):
		key := r.UniqueKey
		if key == "" {
			key = "name"
		}
		httpx.Fail(c, httpx.NewValidationError().Add(key, "The "+key+" has already been taken."))
	case isFKViolation(err):
		httpx.Fail(c, httpx.NewValidationError().Add(r.firstFK(), "The selected combination is invalid."))
	default:
		httpx.Fail(c, err)
	}
}

func (r *Resource) firstFK() string {
	for _, f := range r.Fields {
		if f.Kind == "fk" {
			return f.Key
		}
	}
	return "name"
}

func (r *Resource) remove(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || id < 1 {
			httpx.NotFound(c, "")
			return
		}
		res := db.Exec("DELETE FROM "+r.Table+" WHERE id = ?", id)
		if res.Error != nil {
			if isFKViolation(res.Error) {
				httpx.Abort(c, http.StatusConflict, "This record is in use and cannot be deleted.")
				return
			}
			httpx.Fail(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			httpx.NotFound(c, "")
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Deleted."})
	}
}

// ---- export / import ----------------------------------------------------

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case time.Time:
		return x.Format("2006-01-02")
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

func (r *Resource) export(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := r.rows(db, r.query(c), 0, 0)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		var buf bytes.Buffer
		buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM so Excel reads Vietnamese correctly
		w := csv.NewWriter(&buf)
		head := make([]string, len(r.Export))
		for i, col := range r.Export {
			head[i] = col.Header
		}
		_ = w.Write(head)
		for _, row := range rows {
			rec := make([]string, len(r.Export))
			for i, col := range r.Export {
				rec[i] = cell(row[col.Key])
			}
			_ = w.Write(rec)
		}
		w.Flush()
		c.Header("Content-Disposition", `attachment; filename="`+r.File+`.csv"`)
		c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
	}
}

type importError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

func (r *Resource) importCSV(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		fh, err := c.FormFile("file")
		if err != nil {
			httpx.Fail(c, httpx.NewValidationError().Add("file", "The file field is required."))
			return
		}
		if fh.Size > 5<<20 {
			httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must not be greater than 5120 kilobytes."))
			return
		}
		f, err := fh.Open()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		defer f.Close()
		raw, _ := io.ReadAll(f)
		raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
		cr := csv.NewReader(bytes.NewReader(raw))
		cr.FieldsPerRecord = -1
		cr.TrimLeadingSpace = true
		recs, err := cr.ReadAll()
		if err != nil || len(recs) < 2 {
			httpx.Fail(c, httpx.NewValidationError().Add("file", "The file must be a CSV with a header row and at least one data row."))
			return
		}
		if len(recs) > 2001 {
			httpx.Fail(c, httpx.NewValidationError().Add("file", "The file may contain at most 2000 rows."))
			return
		}
		idx := map[string]int{}
		for i, h := range recs[0] {
			idx[strings.ToLower(strings.TrimSpace(h))] = i
		}
		var errs []importError
		created := 0
		for n, rec := range recs[1:] {
			rowNo := n + 2
			body := map[string]any{}
			for _, fld := range r.Fields {
				col, ok := idx[strings.ToLower(fld.header())]
				if !ok || col >= len(rec) {
					continue
				}
				val := strings.TrimSpace(rec[col])
				if fld.Kind == "fk" && fld.FK != nil && val != "" {
					var id int64
					q := db.Table(fld.FK.Table).Select("id").Where(fld.FK.By+" = ?", val)
					if err := q.Limit(1).Scan(&id).Error; err != nil || id == 0 {
						errs = append(errs, importError{rowNo, fld.header() + ": \"" + val + "\" not found"})
						body = nil
						break
					}
					body[fld.Key] = float64(id)
					continue
				}
				body[fld.Key] = val
			}
			if body == nil {
				continue
			}
			if _, err := r.write(db, 0, body); err != nil {
				errs = append(errs, importError{rowNo, importMessage(err)})
				continue
			}
			created++
		}
		if errs == nil {
			errs = []importError{}
		}
		c.JSON(http.StatusOK, gin.H{"created": created, "failed": len(errs), "errors": errs})
	}
}

func importMessage(err error) string {
	var ve *httpx.ValidationError
	if errors.As(err, &ve) {
		return ve.Message()
	}
	if isDuplicate(err) {
		return "already exists"
	}
	if isFKViolation(err) {
		return "invalid relation (e.g. cohort does not belong to the program)"
	}
	return "could not be saved"
}

// Mount registers the list, export, import, create, update and delete routes of r under g.
func (r *Resource) Mount(g *gin.RouterGroup, db *gorm.DB) {
	g.GET("/"+r.Path, r.list(db))
	g.GET("/"+r.Path+"/export", r.export(db))
	g.POST("/"+r.Path+"/import", r.importCSV(db))
	g.POST("/"+r.Path, r.save(db, false))
	g.PUT("/"+r.Path+"/:id", r.save(db, true))
	g.DELETE("/"+r.Path+"/:id", r.remove(db))
}
