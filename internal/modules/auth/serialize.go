package auth

import (
	"context"
	"time"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

// UserJSON is AuthController::serializeUser.
type UserJSON struct {
	ID              int64        `json:"id"`
	Name            string       `json:"name"`
	Email           string       `json:"email"`
	Avatar          *string      `json:"avatar"`
	Role            *string      `json:"role"`
	Roles           []string     `json:"roles"`
	Permissions     []string     `json:"permissions"`
	EmailVerified   bool         `json:"email_verified"`
	EmailVerifiedAt *httpx.Time  `json:"email_verified_at"`
	UserType        string       `json:"user_type"`
	StudentCode     *string      `json:"student_code"`
	StaffCode       *string      `json:"staff_code"`
	Phone           *string      `json:"phone"`
	Gender          *string      `json:"gender"`
	DateOfBirth     *httpx.Date  `json:"date_of_birth"`
	Hometown        *string      `json:"hometown"`
	PermanentAddr   *string      `json:"permanent_address"`
	Nationality     string       `json:"nationality"`
	Organization    Organization `json:"organization"`
	Academic        Academic     `json:"academic"`
}

// Organization is serializeUser.organization.
type Organization struct {
	InstitutionID   *int64  `json:"institution_id"`
	InstitutionName *string `json:"institution_name"`
	UnitID          *int64  `json:"unit_id"`
	UnitName        *string `json:"unit_name"`
}

// Academic is serializeUser.academic.
type Academic struct {
	ProgramID          *int64  `json:"program_id"`
	ProgramName        *string `json:"program_name"`
	MajorID            *int64  `json:"major_id"`
	MajorName          *string `json:"major_name"`
	SpecializationID   *int64  `json:"specialization_id"`
	SpecializationName *string `json:"specialization_name"`
	CohortID           *int64  `json:"cohort_id"`
	CohortName         *string `json:"cohort_name"`
}

type relationNames struct {
	InstitutionName    *string
	UnitName           *string
	ProgramName        *string
	MajorName          *string
	SpecializationName *string
	CohortName         *string
}

func (m *Module) loadRelationNames(ctx context.Context, u *auth.User) (relationNames, error) {
	var r relationNames
	err := m.db.WithContext(ctx).Raw(`
SELECT
  (SELECT name FROM institutions WHERE id = ?) AS institution_name,
  (SELECT name FROM units WHERE id = ?) AS unit_name,
  (SELECT name FROM programs WHERE id = ?) AS program_name,
  (SELECT name FROM majors WHERE id = ?) AS major_name,
  (SELECT name FROM specializations WHERE id = ?) AS specialization_name,
  (SELECT name FROM cohorts WHERE id = ?) AS cohort_name`,
		u.InstitutionID, u.UnitID, u.ProgramID, u.MajorID, u.SpecializationID, u.CohortID).Scan(&r).Error
	return r, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtr(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	v := httpx.NewTime(t.UTC())
	return &v
}

func datePtr(t *time.Time) *httpx.Date {
	if t == nil {
		return nil
	}
	v := httpx.NewDate(*t)
	return &v
}

// serializeUser builds the legacy user payload.
func (m *Module) serializeUser(ctx context.Context, u *auth.User) (UserJSON, error) {
	roles, perms, err := m.roles.UserRolesPermissions(ctx, u.ID)
	if err != nil {
		return UserJSON{}, err
	}
	rel, err := m.loadRelationNames(ctx, u)
	if err != nil {
		return UserJSON{}, err
	}
	var role *string
	if r := auth.PrimaryRole(roles); r != "" {
		role = &r
	}
	return UserJSON{
		ID:              u.ID,
		Name:            u.Name,
		Email:           u.Email,
		Avatar:          m.media.Public(u.AvatarPath),
		Role:            role,
		Roles:           nonNil(roles),
		Permissions:     nonNil(perms),
		EmailVerified:   u.HasVerifiedEmail(),
		EmailVerifiedAt: timePtr(u.EmailVerifiedAt),
		UserType:        u.UserType,
		StudentCode:     u.StudentCode,
		StaffCode:       u.StaffCode,
		Phone:           u.Phone,
		Gender:          u.Gender,
		DateOfBirth:     datePtr(u.DateOfBirth),
		Hometown:        u.Hometown,
		PermanentAddr:   u.PermanentAddress,
		Nationality:     u.Nationality,
		Organization: Organization{
			InstitutionID: u.InstitutionID, InstitutionName: rel.InstitutionName,
			UnitID: u.UnitID, UnitName: rel.UnitName,
		},
		Academic: Academic{
			ProgramID: u.ProgramID, ProgramName: rel.ProgramName,
			MajorID: u.MajorID, MajorName: rel.MajorName,
			SpecializationID: u.SpecializationID, SpecializationName: rel.SpecializationName,
			CohortID: u.CohortID, CohortName: rel.CohortName,
		},
	}, nil
}

// RawUserJSON is the Eloquent User::toArray() returned by GET /api/user
// (legacy column order; password and remember_token hidden).
type RawUserJSON struct {
	ID                    int64       `json:"id"`
	PointsBalance         int64       `json:"points_balance"`
	StreakDays            int         `json:"streak_days"`
	LastLoginDate         *httpx.Date `json:"last_login_date"`
	StreakLastUpdated     *httpx.Date `json:"streak_last_updated"`
	Name                  string      `json:"name"`
	Email                 string      `json:"email"`
	EmailVerifiedAt       *httpx.Time `json:"email_verified_at"`
	Avatar                *string     `json:"avatar"`
	FaceURL               *string     `json:"face_url"`
	GoogleID              *string     `json:"google_id"`
	Bio                   *string     `json:"bio"`
	InstitutionID         *int64      `json:"institution_id"`
	UnitID                *int64      `json:"unit_id"`
	ProgramID             *int64      `json:"program_id"`
	MajorID               *int64      `json:"major_id"`
	SpecializationID      *int64      `json:"specialization_id"`
	CohortID              *int64      `json:"cohort_id"`
	AdministrativeClassID *int64      `json:"administrative_class_id"`
	AdvisorID             *int64      `json:"advisor_id"`
	UserType              string      `json:"user_type"`
	StudentCode           *string     `json:"student_code"`
	StaffCode             *string     `json:"staff_code"`
	Phone                 *string     `json:"phone"`
	IDCardNumber          *string     `json:"id_card_number"`
	Gender                *string     `json:"gender"`
	DateOfBirth           *httpx.Time `json:"date_of_birth"` // 'date' cast serialises as a full timestamp
	Nationality           string      `json:"nationality"`
	Hometown              *string     `json:"hometown"`
	PermanentAddress      *string     `json:"permanent_address"`
	StudyStatus           *string     `json:"study_status"`
	CreatedAt             httpx.Time  `json:"created_at"`
	UpdatedAt             httpx.Time  `json:"updated_at"`
}

func (m *Module) rawUserJSON(ctx context.Context, u *auth.User) (RawUserJSON, error) {
	var st struct {
		PointsBalance     int64
		StreakDays        int
		LastLoginDate     *time.Time
		StreakLastUpdated *time.Time
		GoogleID          *string
	}
	err := m.db.WithContext(ctx).Raw(`
SELECT coalesce(s.points_balance, 0) AS points_balance, coalesce(s.streak_days, 0) AS streak_days,
       s.last_login_date, s.streak_last_updated,
       (SELECT provider_subject FROM user_identities WHERE user_id = ? AND provider = 'google') AS google_id
FROM (SELECT 1) one LEFT JOIN user_stats s ON s.user_id = ?`, u.ID, u.ID).Scan(&st).Error
	if err != nil {
		return RawUserJSON{}, err
	}
	return RawUserJSON{
		ID: u.ID, PointsBalance: st.PointsBalance, StreakDays: st.StreakDays,
		LastLoginDate: datePtr(st.LastLoginDate), StreakLastUpdated: datePtr(st.StreakLastUpdated),
		Name: u.Name, Email: u.Email, EmailVerifiedAt: timePtr(u.EmailVerifiedAt),
		Avatar: m.media.Public(u.AvatarPath), FaceURL: m.media.Public(u.FaceImagePath),
		GoogleID: st.GoogleID, Bio: u.Bio,
		InstitutionID: u.InstitutionID, UnitID: u.UnitID, ProgramID: u.ProgramID, MajorID: u.MajorID,
		SpecializationID: u.SpecializationID, CohortID: u.CohortID,
		AdministrativeClassID: u.AdministrativeClassID, AdvisorID: u.AdvisorID,
		UserType: u.UserType, StudentCode: u.StudentCode, StaffCode: u.StaffCode, Phone: u.Phone,
		IDCardNumber: u.IDCardNumber, Gender: u.Gender, DateOfBirth: timePtr(u.DateOfBirth),
		Nationality: u.Nationality, Hometown: u.Hometown, PermanentAddress: u.PermanentAddress,
		StudyStatus: u.StudyStatus,
		CreatedAt:   httpx.NewTime(u.CreatedAt.UTC()), UpdatedAt: httpx.NewTime(u.UpdatedAt.UTC()),
	}, nil
}
