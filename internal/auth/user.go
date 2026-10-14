package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ErrUserNotFound is returned for missing and soft-deleted users.
var ErrUserNotFound = errors.New("auth: user not found")

// User maps the users table (search_text, a generated column, is omitted).
// DeletedAt is a gorm.DeletedAt, so model queries skip soft-deleted rows.
type User struct {
	ID                    int64          `gorm:"column:id;primaryKey"`
	Email                 string         `gorm:"column:email"`
	PasswordHash          *string        `gorm:"column:password_hash"`
	EmailVerifiedAt       *time.Time     `gorm:"column:email_verified_at"`
	Name                  string         `gorm:"column:name"`
	UserType              string         `gorm:"column:user_type;default:student"`
	AvatarPath            *string        `gorm:"column:avatar_path"`
	FaceImagePath         *string        `gorm:"column:face_image_path"`
	Bio                   *string        `gorm:"column:bio"`
	InstitutionID         *int64         `gorm:"column:institution_id"`
	UnitID                *int64         `gorm:"column:unit_id"`
	StaffCode             *string        `gorm:"column:staff_code"`
	StudentCode           *string        `gorm:"column:student_code"`
	ProgramID             *int64         `gorm:"column:program_id"`
	MajorID               *int64         `gorm:"column:major_id"`
	SpecializationID      *int64         `gorm:"column:specialization_id"`
	CohortID              *int64         `gorm:"column:cohort_id"`
	AdministrativeClassID *int64         `gorm:"column:administrative_class_id"`
	AdvisorID             *int64         `gorm:"column:advisor_id"`
	StudyStatus           *string        `gorm:"column:study_status"`
	Phone                 *string        `gorm:"column:phone"`
	IDCardNumber          *string        `gorm:"column:id_card_number"`
	Gender                *string        `gorm:"column:gender"`
	DateOfBirth           *time.Time     `gorm:"column:date_of_birth"`
	Nationality           string         `gorm:"column:nationality;default:Việt Nam"`
	Hometown              *string        `gorm:"column:hometown"`
	PermanentAddress      *string        `gorm:"column:permanent_address"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
	UpdatedAt             time.Time      `gorm:"column:updated_at"`
	DeletedAt             gorm.DeletedAt `gorm:"column:deleted_at"`
	DisabledAt            *time.Time     `gorm:"column:disabled_at"`
}

// TableName implements gorm's tabler.
func (User) TableName() string { return "users" }

// IsDisabled reports whether an admin has locked the account.
func (u *User) IsDisabled() bool { return u.DisabledAt != nil }

// HasVerifiedEmail mirrors MustVerifyEmail::hasVerifiedEmail.
func (u *User) HasVerifiedEmail() bool { return u.EmailVerifiedAt != nil }

// PasswordHashValue returns the hash or "" for OAuth-only accounts.
func (u *User) PasswordHashValue() string {
	if u.PasswordHash == nil {
		return ""
	}
	return *u.PasswordHash
}

// FindUserByID loads an active (not soft-deleted) user.
func FindUserByID(ctx context.Context, db *gorm.DB, id int64) (*User, error) {
	var u User
	err := db.WithContext(ctx).Where("id = ?", id).Take(&u).Error
	return userResult(&u, err)
}

// FindUserByEmail loads an active user by e-mail (case-insensitive, citext).
func FindUserByEmail(ctx context.Context, db *gorm.DB, email string) (*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, ErrUserNotFound
	}
	var u User
	err := db.WithContext(ctx).Where("email = ?", email).Take(&u).Error
	return userResult(&u, err)
}

// EmailExists reports whether any user, including soft-deleted ones, holds
// email (the unique constraint keeps deleted users' addresses reserved).
func EmailExists(ctx context.Context, db *gorm.DB, email string) (bool, error) {
	var ok bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM users WHERE email = ?)`, email).Scan(&ok).Error
	return ok, err
}

func userResult(u *User, err error) (*User, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}
