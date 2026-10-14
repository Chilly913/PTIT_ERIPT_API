-- Removes exactly the reference rows inserted by 000092_rbac_reference_data.up.sql.
-- Deleting a role/permission cascades to its role_permissions / user_roles / user_permissions rows.

DELETE FROM role_permissions rp
USING roles r, permissions p
WHERE rp.role_id = r.id
  AND rp.permission_id = p.id
  AND r.name IN ('admin', 'academic_manager', 'instructor', 'advisor', 'finance', 'student')
  AND p.name IN ('view_dashboard', 'manage_users', 'manage_roles', 'manage_courses', 'manage_lessons',
                 'manage_exams', 'manage_finances', 'manage_academic', 'manage_enrollments', 'manage_grades',
                 'view_grades', 'advise_students', 'take_exams', 'view_reports', 'submit_reviews');

DELETE FROM roles
WHERE name IN ('admin', 'academic_manager', 'instructor', 'advisor', 'finance', 'student');

DELETE FROM permissions
WHERE name IN ('view_dashboard', 'manage_users', 'manage_roles', 'manage_courses', 'manage_lessons',
               'manage_exams', 'manage_finances', 'manage_academic', 'manage_enrollments', 'manage_grades',
               'view_grades', 'advise_students', 'take_exams', 'view_reports', 'submit_reviews');
