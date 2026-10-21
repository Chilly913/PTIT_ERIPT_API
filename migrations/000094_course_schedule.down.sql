ALTER TABLE sections DROP CONSTRAINT ck_sections_window, DROP COLUMN opens_at, DROP COLUMN closes_at;
ALTER TABLE courses DROP CONSTRAINT ck_courses_window, DROP CONSTRAINT ck_courses_learning_mode,
    DROP COLUMN opens_at, DROP COLUMN closes_at, DROP COLUMN learning_mode;
