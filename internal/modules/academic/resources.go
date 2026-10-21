package academic

import (
	"gorm.io/gorm"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/crud"
)

func extraInstitution(tx *gorm.DB) (map[string]any, error) {
	id, err := defaultInstitution(tx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"institution_id": id}, nil
}

// clearCurrent unsets the "current" flag on sibling rows before a row is marked current
// (the schema allows a single current row per scope).
func clearCurrent(table, scopeCol, scopeKey string) func(tx *gorm.DB, id int64, v map[string]any) error {
	return func(tx *gorm.DB, id int64, v map[string]any) error {
		if cur, _ := v["is_current"].(bool); !cur {
			return nil
		}
		q := tx.Table(table).Where("is_current AND id <> ?", id)
		if scopeCol != "" {
			if sv, ok := v[scopeKey]; ok && sv != nil {
				q = q.Where(scopeCol+" = ?", sv)
			}
		}
		return q.UpdateColumn("is_current", false).Error
	}
}

func dateOrder(startKey, endKey, msg string) func(v map[string]any) map[string]string {
	return func(v map[string]any) map[string]string {
		s, _ := v[startKey].(string)
		e, _ := v[endKey].(string)
		if s != "" && e != "" && s >= e {
			return map[string]string{endKey: msg}
		}
		return nil
	}
}

var (
	termStatuses  = []string{"planned", "upcoming", "ongoing", "active", "completed"}
	yearStatuses  = []string{"planned", "active", "inactive", "completed"}
	cohortStates  = []string{"active", "inactive", "graduated"}
	classStatuses = []string{"active", "graduated", "suspended"}
)

var resources = []*crud.Resource{
	{
		Path: "program-types", Table: "program_types", File: "he-dao-tao", UniqueKey: "code",
		Fields: []crud.Field{
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 150},
			{Key: "code", Col: "code", Kind: "text", Required: true, Max: 50},
			{Key: "description", Col: "description", Kind: "text", Max: 1000},
			{Key: "is_active", Col: "is_active", Kind: "bool", DefBool: true},
		},
		Select: `t.id, t.name, t.code, t.description, t.is_active, t.created_at,
			(SELECT count(*) FROM programs p WHERE p.program_type_id = t.id) AS programs_count`,
		From:        "program_types t",
		Search:      []string{"t.name", "t.code", "t.description"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "code": "t.code", "programs_count": "programs_count", "created_at": "t.created_at"},
		DefaultSort: "name",
		Filters:     []crud.Filter{{Param: "is_active", Expr: "t.is_active = ?", Bool: true}},
		Export:      []crud.ExportCol{{Header: "code", Key: "code"}, {Header: "name", Key: "name"}, {Header: "description", Key: "description"}, {Header: "is_active", Key: "is_active"}},
	},
	{
		Path: "programs", Table: "programs", File: "chuong-trinh-dao-tao", UniqueKey: "code", Extra: extraInstitution,
		Fields: []crud.Field{
			{Key: "program_type_id", Col: "program_type_id", Kind: "fk", Required: true, FK: &crud.FK{Table: "program_types", By: "code"}, Import: "program_type_code"},
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 200},
			{Key: "code", Col: "code", Kind: "text", Required: true, Max: 50},
			{Key: "duration_months", Col: "duration_months", Kind: "int", Min: 1, Max: 240},
			{Key: "is_active", Col: "is_active", Kind: "bool", DefBool: true},
		},
		Select: `t.id, t.program_type_id, pt.name AS program_type_name, pt.code AS program_type_code, t.name, t.code,
			t.duration_months, t.is_active, t.created_at`,
		From:        "programs t JOIN program_types pt ON pt.id = t.program_type_id",
		Search:      []string{"t.name", "t.code", "pt.name"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "code": "t.code", "program_type_name": "pt.name", "duration_months": "t.duration_months", "created_at": "t.created_at"},
		DefaultSort: "code",
		Filters: []crud.Filter{
			{Param: "program_type_id", Expr: "t.program_type_id = ?::bigint"},
			{Param: "is_active", Expr: "t.is_active = ?", Bool: true},
		},
		Export: []crud.ExportCol{{Header: "program_type_code", Key: "program_type_code"}, {Header: "code", Key: "code"}, {Header: "name", Key: "name"}, {Header: "duration_months", Key: "duration_months"}, {Header: "is_active", Key: "is_active"}},
	},
	{
		Path: "academic-years", Table: "academic_years", File: "nam-hoc", UniqueKey: "name", Extra: extraInstitution,
		Fields: []crud.Field{
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 100},
			{Key: "start_date", Col: "start_date", Kind: "date", Required: true},
			{Key: "end_date", Col: "end_date", Kind: "date", Required: true},
			{Key: "status", Col: "status", Kind: "enum", Required: true, Enum: yearStatuses},
			{Key: "is_current", Col: "is_current", Kind: "bool"},
		},
		Select:      `t.id, t.name, t.start_date::text AS start_date, t.end_date::text AS end_date, t.status, t.is_current, t.created_at`,
		From:        "academic_years t",
		Search:      []string{"t.name"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "start_date": "t.start_date", "end_date": "t.end_date", "status": "t.status", "created_at": "t.created_at"},
		DefaultSort: "start_date",
		Filters:     []crud.Filter{{Param: "status", Expr: "t.status = ?"}, {Param: "is_current", Expr: "t.is_current = ?", Bool: true}},
		Check:       dateOrder("start_date", "end_date", "The end date must be after the start date."),
		Before:      clearCurrent("academic_years", "", ""),
		Export:      []crud.ExportCol{{Header: "name", Key: "name"}, {Header: "start_date", Key: "start_date"}, {Header: "end_date", Key: "end_date"}, {Header: "status", Key: "status"}, {Header: "is_current", Key: "is_current"}},
	},
	{
		Path: "terms", Table: "terms", File: "hoc-ky", UniqueKey: "code",
		Fields: []crud.Field{
			{Key: "academic_year_id", Col: "academic_year_id", Kind: "fk", Required: true, FK: &crud.FK{Table: "academic_years", By: "name"}, Import: "academic_year"},
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 150},
			{Key: "code", Col: "code", Kind: "text", Required: true, Max: 50},
			{Key: "start_date", Col: "start_date", Kind: "date", Required: true},
			{Key: "end_date", Col: "end_date", Kind: "date", Required: true},
			{Key: "status", Col: "status", Kind: "enum", Required: true, Enum: termStatuses},
			{Key: "is_current", Col: "is_current", Kind: "bool"},
		},
		Select: `t.id, t.academic_year_id, ay.name AS academic_year, t.name, t.code, t.start_date::text AS start_date,
			t.end_date::text AS end_date, t.status, t.is_current, t.created_at`,
		From:        "terms t JOIN academic_years ay ON ay.id = t.academic_year_id",
		Search:      []string{"t.name", "t.code", "ay.name"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "code": "t.code", "academic_year": "ay.name", "start_date": "t.start_date", "end_date": "t.end_date", "status": "t.status", "created_at": "t.created_at"},
		DefaultSort: "start_date",
		Filters: []crud.Filter{
			{Param: "academic_year_id", Expr: "t.academic_year_id = ?::bigint"},
			{Param: "status", Expr: "t.status = ?"},
			{Param: "is_current", Expr: "t.is_current = ?", Bool: true},
		},
		Check:  dateOrder("start_date", "end_date", "The end date must be after the start date."),
		Before: clearCurrent("terms", "academic_year_id", "academic_year_id"),
		Export: []crud.ExportCol{{Header: "academic_year", Key: "academic_year"}, {Header: "code", Key: "code"}, {Header: "name", Key: "name"}, {Header: "start_date", Key: "start_date"}, {Header: "end_date", Key: "end_date"}, {Header: "status", Key: "status"}, {Header: "is_current", Key: "is_current"}},
	},
	{
		Path: "cohorts", Table: "cohorts", File: "khoa", UniqueKey: "code", Extra: extraInstitution,
		Fields: []crud.Field{
			{Key: "program_id", Col: "program_id", Kind: "fk", Required: true, FK: &crud.FK{Table: "programs", By: "code"}, Import: "program_code"},
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 150},
			{Key: "code", Col: "code", Kind: "text", Required: true, Max: 50},
			{Key: "start_year", Col: "start_year", Kind: "int", Required: true, Min: 1900, Max: 2200},
			{Key: "end_year", Col: "end_year", Kind: "int", Min: 1900, Max: 2200},
			{Key: "status", Col: "status", Kind: "enum", Required: true, Enum: cohortStates},
		},
		Select:      `t.id, t.program_id, p.name AS program_name, p.code AS program_code, t.name, t.code, t.start_year, t.end_year, t.status, t.created_at`,
		From:        "cohorts t JOIN programs p ON p.id = t.program_id",
		Search:      []string{"t.name", "t.code", "p.name", "p.code"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "code": "t.code", "program_name": "p.name", "start_year": "t.start_year", "end_year": "t.end_year", "status": "t.status", "created_at": "t.created_at"},
		DefaultSort: "start_year",
		Filters: []crud.Filter{
			{Param: "program_id", Expr: "t.program_id = ?::bigint"},
			{Param: "status", Expr: "t.status = ?"},
			{Param: "start_year", Expr: "t.start_year = ?::int"},
		},
		Check: func(v map[string]any) map[string]string {
			s, _ := v["start_year"].(int64)
			e, ok := v["end_year"].(int64)
			if ok && e < s {
				return map[string]string{"end_year": "The end year must be greater than or equal to the start year."}
			}
			return nil
		},
		Export: []crud.ExportCol{{Header: "program_code", Key: "program_code"}, {Header: "code", Key: "code"}, {Header: "name", Key: "name"}, {Header: "start_year", Key: "start_year"}, {Header: "end_year", Key: "end_year"}, {Header: "status", Key: "status"}},
	},
	{
		Path: "classes", Table: "administrative_classes", File: "lop-hanh-chinh", UniqueKey: "code", Extra: extraInstitution,
		Fields: []crud.Field{
			{Key: "program_id", Col: "program_id", Kind: "fk", Required: true, FK: &crud.FK{Table: "programs", By: "code"}, Import: "program_code"},
			{Key: "cohort_id", Col: "cohort_id", Kind: "fk", FK: &crud.FK{Table: "cohorts", By: "code"}, Import: "cohort_code"},
			{Key: "advisor_id", Col: "advisor_id", Kind: "fk", FK: &crud.FK{Table: "users", By: "email"}, Import: "advisor_email"},
			{Key: "code", Col: "code", Kind: "text", Required: true, Max: 50},
			{Key: "name", Col: "name", Kind: "text", Required: true, Max: 200},
			{Key: "expected_graduation_year", Col: "expected_graduation_year", Kind: "int", Min: 1900, Max: 2200},
			{Key: "capacity", Col: "capacity", Kind: "int", Min: 0, Max: 100000, Default: int64(0)},
			{Key: "status", Col: "status", Kind: "enum", Required: true, Enum: classStatuses},
			{Key: "description", Col: "description", Kind: "text", Max: 1000},
		},
		Select: `t.id, t.program_id, p.name AS program_name, p.code AS program_code, t.cohort_id, c.name AS cohort_name, c.code AS cohort_code,
			t.advisor_id, adv.name AS advisor_name, adv.email AS advisor_email, t.code, t.name, t.expected_graduation_year, t.capacity, t.status, t.description, t.created_at`,
		From:        "administrative_classes t JOIN programs p ON p.id = t.program_id LEFT JOIN cohorts c ON c.id = t.cohort_id LEFT JOIN users adv ON adv.id = t.advisor_id",
		Search:      []string{"t.name", "t.code", "p.name", "c.name"},
		Sort:        map[string]string{"id": "t.id", "name": "t.name", "code": "t.code", "program_name": "p.name", "cohort_name": "c.name", "advisor_name": "adv.name", "capacity": "t.capacity", "expected_graduation_year": "t.expected_graduation_year", "status": "t.status", "created_at": "t.created_at"},
		DefaultSort: "code",
		Filters: []crud.Filter{
			{Param: "program_id", Expr: "t.program_id = ?::bigint"},
			{Param: "cohort_id", Expr: "t.cohort_id = ?::bigint"},
			{Param: "advisor_id", Expr: "t.advisor_id = ?::bigint"},
			{Param: "status", Expr: "t.status = ?"},
		},
		Export: []crud.ExportCol{{Header: "program_code", Key: "program_code"}, {Header: "cohort_code", Key: "cohort_code"}, {Header: "advisor_email", Key: "advisor_email"}, {Header: "code", Key: "code"}, {Header: "name", Key: "name"}, {Header: "expected_graduation_year", Key: "expected_graduation_year"}, {Header: "capacity", Key: "capacity"}, {Header: "status", Key: "status"}, {Header: "description", Key: "description"}},
	},
}
