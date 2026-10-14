-- 000092_rbac_reference_data: canonical RBAC catalogue (reference data, not dev seed).
-- Mirrors legacy database/seeders/RoleSeeder.php exactly: 6 roles, 15 permissions and
-- the role -> permission matrix. Idempotent (ON CONFLICT DO NOTHING); rows added later
-- by admins (role permission edits) are left untouched.
-- Go mirror: internal/rbac (AllRoles, AllPermissions, DefaultMatrix); docs/RBAC.md.

INSERT INTO roles (name) VALUES
    ('admin'),
    ('academic_manager'),
    ('instructor'),
    ('advisor'),
    ('finance'),
    ('student')
ON CONFLICT (name) DO NOTHING;

INSERT INTO permissions (name) VALUES
    ('view_dashboard'),
    ('manage_users'),
    ('manage_roles'),
    ('manage_courses'),
    ('manage_lessons'),
    ('manage_exams'),
    ('manage_finances'),
    ('manage_academic'),
    ('manage_enrollments'),
    ('manage_grades'),
    ('view_grades'),
    ('advise_students'),
    ('take_exams'),
    ('view_reports'),
    ('submit_reviews')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM (VALUES
    -- admin: every permission
    ('admin', 'view_dashboard'),
    ('admin', 'manage_users'),
    ('admin', 'manage_roles'),
    ('admin', 'manage_courses'),
    ('admin', 'manage_lessons'),
    ('admin', 'manage_exams'),
    ('admin', 'manage_finances'),
    ('admin', 'manage_academic'),
    ('admin', 'manage_enrollments'),
    ('admin', 'manage_grades'),
    ('admin', 'view_grades'),
    ('admin', 'advise_students'),
    ('admin', 'take_exams'),
    ('admin', 'view_reports'),
    ('admin', 'submit_reviews'),
    -- academic_manager
    ('academic_manager', 'view_dashboard'),
    ('academic_manager', 'manage_academic'),
    ('academic_manager', 'manage_enrollments'),
    ('academic_manager', 'manage_grades'),
    ('academic_manager', 'view_grades'),
    ('academic_manager', 'view_reports'),
    -- instructor (manage_academic: academic years / terms + administrative classes, QR attendance)
    ('instructor', 'view_dashboard'),
    ('instructor', 'manage_courses'),
    ('instructor', 'manage_lessons'),
    ('instructor', 'manage_exams'),
    ('instructor', 'manage_grades'),
    ('instructor', 'view_grades'),
    ('instructor', 'view_reports'),
    ('instructor', 'manage_academic'),
    -- advisor
    ('advisor', 'view_dashboard'),
    ('advisor', 'advise_students'),
    ('advisor', 'view_grades'),
    ('advisor', 'view_reports'),
    -- finance
    ('finance', 'view_dashboard'),
    ('finance', 'manage_finances'),
    ('finance', 'view_reports'),
    -- student
    ('student', 'view_grades'),
    ('student', 'take_exams'),
    ('student', 'submit_reviews')
) AS m (role_name, permission_name)
JOIN roles r ON r.name = m.role_name
JOIN permissions p ON p.name = m.permission_name
ON CONFLICT DO NOTHING;
