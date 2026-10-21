-- 000094_course_schedule: open/close windows for a course and its sections, and the learning order.
-- learning_mode: 'free' = learners may open any lesson; 'sequential' = lessons unlock in order as the previous one is completed.
ALTER TABLE courses
    ADD COLUMN opens_at      timestamptz,
    ADD COLUMN closes_at     timestamptz,
    ADD COLUMN learning_mode text NOT NULL DEFAULT 'free',
    ADD CONSTRAINT ck_courses_learning_mode CHECK (learning_mode IN ('free', 'sequential')),
    ADD CONSTRAINT ck_courses_window CHECK (closes_at IS NULL OR opens_at IS NULL OR closes_at > opens_at);

ALTER TABLE sections
    ADD COLUMN opens_at  timestamptz,
    ADD COLUMN closes_at timestamptz,
    ADD CONSTRAINT ck_sections_window CHECK (closes_at IS NULL OR opens_at IS NULL OR closes_at > opens_at);
