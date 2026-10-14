-- users -> academic tables created in 000004_academic.
ALTER TABLE users
    ADD CONSTRAINT fk_users_program_id FOREIGN KEY (program_id)
        REFERENCES programs (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_users_major_id FOREIGN KEY (major_id)
        REFERENCES majors (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_users_specialization_id FOREIGN KEY (specialization_id)
        REFERENCES specializations (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_users_cohort_id FOREIGN KEY (cohort_id)
        REFERENCES cohorts (id) ON DELETE SET NULL,
    ADD CONSTRAINT fk_users_administrative_class_id FOREIGN KEY (administrative_class_id)
        REFERENCES administrative_classes (id) ON DELETE SET NULL;
