ALTER TABLE IF EXISTS enrollments
    DROP CONSTRAINT IF EXISTS fk_enrollments_order_id,
    DROP CONSTRAINT IF EXISTS fk_enrollments_class_section_id,
    DROP CONSTRAINT IF EXISTS fk_enrollments_cohort_id,
    DROP CONSTRAINT IF EXISTS fk_enrollments_term_id;

ALTER TABLE IF EXISTS courses
    DROP CONSTRAINT IF EXISTS fk_courses_certificate_template_id,
    DROP CONSTRAINT IF EXISTS fk_courses_major_id,
    DROP CONSTRAINT IF EXISTS fk_courses_program_id;
