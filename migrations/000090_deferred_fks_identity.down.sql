ALTER TABLE IF EXISTS users
    DROP CONSTRAINT IF EXISTS fk_users_administrative_class_id,
    DROP CONSTRAINT IF EXISTS fk_users_cohort_id,
    DROP CONSTRAINT IF EXISTS fk_users_specialization_id,
    DROP CONSTRAINT IF EXISTS fk_users_major_id,
    DROP CONSTRAINT IF EXISTS fk_users_program_id;
