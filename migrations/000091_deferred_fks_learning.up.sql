-- 000090_deferred_fks_learning
-- FKs from 000003_learning tables to tables created in later files
-- (000004_academic: programs, majors, terms, cohorts, class_sections;
--  000006_commerce_career: certificate_templates, orders).
-- Supporting indexes already exist in 000003.

ALTER TABLE courses
    ADD CONSTRAINT fk_courses_program_id FOREIGN KEY (program_id)
        REFERENCES programs (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_courses_major_id FOREIGN KEY (major_id)
        REFERENCES majors (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_courses_certificate_template_id FOREIGN KEY (certificate_template_id)
        REFERENCES certificate_templates (id) ON DELETE SET NULL;

ALTER TABLE enrollments
    ADD CONSTRAINT fk_enrollments_term_id FOREIGN KEY (term_id)
        REFERENCES terms (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_enrollments_cohort_id FOREIGN KEY (cohort_id)
        REFERENCES cohorts (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_enrollments_class_section_id FOREIGN KEY (class_section_id)
        REFERENCES class_sections (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_enrollments_order_id FOREIGN KEY (order_id)
        REFERENCES orders (id) ON DELETE SET NULL;
