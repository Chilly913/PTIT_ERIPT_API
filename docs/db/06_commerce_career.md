# 06 — commerce_career (`000006_commerce_career`)

References: `users` (000002), `courses` (000003), `terms` (000004).
Nothing points to a later file, so there is **no** `000090_deferred_fks_commerce`.

Validated on PostgreSQL 17 (scratch DB): 000001 + stubs + up -> down -> up -> down -> up, all with
`ON_ERROR_STOP`, plus behavioural checks (webhook double delivery, voucher lock, ledger immutability,
cascade from `users`, citext credential lookup).

## 1. Tables

| Table | Purpose |
|---|---|
| `certificate_templates` | Certificate background + field layout (jsonb array). |
| `career_paths` | Sellable/followable learning path (bundle of courses). |
| `career_path_courses` | Ordered courses inside a path, required/optional, milestone label. |
| `vouchers` | Point-shop catalogue item (discount %, fixed, free course, gift, AI quota). |
| `user_vouchers` | One redemption of a voucher by a user; `code` is the coupon. |
| `orders` | Payment header: user, totals, voucher, gateway identifiers, status. |
| `order_items` | **New.** What was purchased (course or career path) with price snapshot. |
| `user_career_paths` | User following/purchased/completed a path + progress. |
| `user_certificates` | Issued certificate (course or path), public `credential_id`. |
| `tuitions` | Fixed tuition fee per student per term (unpaid/paid). |
| `point_transactions` | Append-only points ledger (signed amounts). |
| `user_point_balances` (view) | Balance / total earned / total redeemed derived from the ledger. |
| `job_postings` | Reference job ads (seeded) used by recommendations. |
| `user_cvs` | Uploaded or form-built CV + AI parse/evaluation (jsonb). |
| `career_recommendations` | AI career-advisor result history per user. |

## 2. Changes vs legacy

### Orders / payments
- **`order_items` replaces `orders.course_id`, `orders.career_path_id` and `orders.cart_items` json.**
  Legacy stored the first cart course in `course_id` *and* the whole cart in `cart_items` json; instructor
  revenue (`InstructorController`) summed `orders.amount WHERE course_id = X`, so a multi-course cart was
  credited entirely to its first course. With items, per-course revenue is `SUM(order_items.amount)` (exact,
  indexed by `course_id`). A pair of nullable FKs on `orders` was rejected because carts are multi-course.
  Each item has `item_type` + matching FK (`ck_order_items_target`); FKs are `ON DELETE SET NULL` and
  `title`/`price` are snapshots, so deleting a course never destroys financial history.
  Rule kept in Go (not DB): one order is either 1..n course items or exactly 1 career_path item.
- **Gateway identifiers split.** Legacy `payment_ref` held the PayOS `paymentLinkId` while pending, then was
  overwritten with the bank `reference` when paid, or `FREE_xxx`/`MOMO_xxx` mocks. Now:
  `provider_order_code` (PayOS `orderCode`, the webhook lookup key; legacy used `orders.id`),
  `payment_link_id`, `provider_txn_ref`; each `UNIQUE (payment_method, <col>)`.
- **Webhook idempotency**: transition is a single conditional update
  `UPDATE orders SET status='paid', paid_at=now(), provider_txn_ref=$ref, gateway_response=$p
   WHERE payment_method='payos' AND provider_order_code=$code AND status='pending' RETURNING id;`
  0 rows -> already processed (return 200, do not fulfil again). A repeated `reference` on another order
  fails `uq_orders_provider_txn_ref`. Tested: second delivery updates 0 rows.
- `status` CHECK `pending|paid|failed|cancelled|refunded`; `payment_method` CHECK
  `payos|momo|zalopay|bank_transfer|free` (from `OrderController` validation + `free`).
- Money `numeric(14,2)` (legacy `decimal(12,0)`); `ck_orders_amounts`: `amount = original_amount - discount_amount`,
  all >= 0. `original_amount` is now NOT NULL (legacy NULL for path orders -> = amount).
  `ck_orders_paid_at`: paid/refunded need `paid_at`. `ck_orders_free_is_zero`.
- **Voucher locking in DB**: `uq_orders_user_voucher_id_active` (partial unique on `user_voucher_id` where status in
  pending/paid) replaces the "locked voucher" queries in `CheckoutService` and prevents double use under races.
- `orders.user_id` `ON DELETE RESTRICT` (legacy CASCADE): financial records must not vanish with a user. See Q1.
- Indexes: `(user_id, created_at DESC)` (my orders), `(status, created_at DESC)` (admin list),
  partial `(paid_at) WHERE status='paid'` (revenue). Legacy duplicate `payment_ref` index dropped.

### Vouchers
- `vouchers` has no code in legacy (it is a catalogue); the coupon code lives on `user_vouchers.code`, now
  `citext UNIQUE` (case-insensitive entry).
- **New** `vouchers.starts_at` (nullable) + `ck_vouchers_validity_window (starts_at < expires_at)`.
- Usage limits enforced: `total_quantity >= 1 or NULL`, `0 <= redeemed_count <= total_quantity`.
- `discount_value` -> `numeric(14,2)`; required for percent/fixed types, `<= 100` for percent.
- `user_vouchers.voucher_id` `ON DELETE RESTRICT` (legacy CASCADE) — mirrors the app rule "cannot delete a voucher
  that was redeemed". `ck_user_vouchers_used_at`: `used` <=> `used_at`.
- **Dropped `user_vouchers.order_id`**: redundant reverse of `orders.user_voucher_id` (written only in
  `CheckoutService::fulfill` with the same pair). API field `order_id` = the paid order with that `user_voucher_id`
  (unique thanks to the partial index). Removes a circular FK.
- `image` -> `image_path` (object key). Dropped low-selectivity indexes on `is_active`, `type`; added partial
  `(points_cost) WHERE is_active` for the shop listing.

### Points ledger
- Append-only: trigger `point_transactions_append_only` rejects UPDATE/DELETE, except RI-cascaded actions
  (user delete, `user_voucher_id SET NULL`). `updated_at` dropped (rows never change).
- `type` CHECK `earn|redeem`, `action` CHECK (the 10 keys from `PointService::POINTS` + `redeem_voucher`),
  sign CHECK (earn > 0, redeem < 0).
- **Polymorphic `referenceable_type/_id` dropped** — never populated (0 of 1 513 dev rows, only daily-login awards
  call `award()` and pass no reference). Replaced by an explicit `user_voucher_id` (link a redeem to the voucher it
  bought) and `dedupe_key` with `UNIQUE (user_id, dedupe_key)` to make awards idempotent
  (`login_daily:<date>`, `lesson_complete:<lesson_id>`, ...), fixing the daily-login race.
- Balance: `users.points_balance` (identity domain) stays as a cache updated in the same transaction as the
  insert (leaderboard sorts on it). `user_point_balances` view is the authoritative derivation for
  reconciliation and `total_earned` (`PointsController::quests`).
- Index `(user_id, created_at DESC, id DESC)` + `(created_at)` for the admin 14-day trend. `type` index dropped.

### Career paths / certificates
- `price` -> `numeric(14,2)`; `status` CHECK `draft|published|archived`; published needs `published_at`.
- `cover_url` -> `cover_path` (object key; absolute URL tolerated — seeded data uses Unsplash URLs).
- Trigram GIN on `title`, `target_role` (admin/public `LIKE` search); `(status, published_at DESC, id DESC)` for
  the public catalogue.
- `user_career_paths`: status CHECK `following|purchased|completed`, `progress_percent` 0..100, completed needs
  `completed_at`.
- `user_certificates.credential_id` `citext UNIQUE` (public `/certificates/verify/{credentialId}` is
  case-insensitive). `ck_user_certificates_subject` (at most one of course/path). Partial uniques
  `(user_id, course_id)` / `(user_id, career_path_id)` make the `firstOrCreate` issuing race-free.
  Template FK `RESTRICT` (legacy CASCADE would delete issued certificates when a template is removed).
- `certificate_templates.background_image_url` -> `background_image_path`; `fields_config` NOT NULL default `[]`.

### Tuitions
- Used by `TuitionController` (student list/pay, admin CRUD/mark-paid); `amount` numeric(14,2) >= 0.
- `term_id` now NOT NULL + `RESTRICT` (validation always requires it; 0 NULLs in 1 049 dev rows; deleting a term
  must not orphan debts). `ck_tuitions_paid_at`: `paid` <=> `paid_at` (controller already keeps them in sync).
- `order_id` kept (never written today; controller comment plans PayOS tuition) and now has an FK to `orders`.
- Index `(term_id, status)` for the admin filter; `(user_id, …)` covered by the unique key.

### CV / AI career
- `user_cvs.source` CHECK `upload|form`; upload requires `file_path`. `skills` jsonb array NOT NULL default `[]`.
  `profile_json` -> `profile`, `evaluation_json` -> `evaluation` (jsonb objects). `expected_salary` -> numeric(14,2).
- `uq_user_cvs_user_id_form`: one form CV per user (legacy `updateOrCreate(user, source='form', file_name='cv-form.json')`).
- `(user_id, created_at DESC, id DESC)` serves `latestCv` (`latestOfMany`).
- `career_recommendations.ai_summary` (text containing JSON) -> `analysis jsonb` object. New nullable
  `target_role` (role the analysis was made for; previously only implicit). `match_score` smallint 0..100.
  `skill_gaps`, `suggested_courses` jsonb arrays NOT NULL (ordered course-id snapshot for display).
- `job_postings` kept (seeded, referenced by `career_recommendations.job_id`, serialized as `job`).

## 3. Old -> new column mapping

| Legacy | New | Note |
|---|---|---|
| orders.id | orders.id; also orders.provider_order_code when payment_method='payos' | legacy PayOS orderCode = id |
| orders.user_id | orders.user_id | |
| orders.course_id / cart_items[] | order_items (item_type='course', course_id, title, price, discount_amount←discount, amount←payable, sort_order←array index) | if cart_items NULL: one item from course_id (price=amount) |
| orders.career_path_id | order_items (item_type='career_path', career_path_id, title←career_paths.title, price=amount) | |
| orders.amount | orders.amount | |
| orders.original_amount | orders.original_amount | NULL -> amount + discount_amount |
| orders.discount_amount | orders.discount_amount | |
| orders.user_voucher_id | orders.user_voucher_id | |
| orders.status | orders.status | 'completed' (read-only alias in legacy) -> 'paid' |
| orders.payment_method | orders.payment_method | |
| orders.payment_ref | status='paid' & payos: provider_txn_ref; pending/failed payos: payment_link_id; FREE_/MOMO_/… mocks: provider_txn_ref | API `payment_ref` = COALESCE(provider_txn_ref, payment_link_id) |
| orders.gateway_response | orders.gateway_response | jsonb |
| orders.paid_at, created_at, updated_at | same | |
| vouchers.* | vouchers.* | image -> image_path; discount_value int -> numeric; new starts_at = NULL |
| user_vouchers.code | user_vouchers.code (citext) | |
| user_vouchers.order_id | dropped | derive: orders WHERE user_voucher_id = uv.id AND status='paid' |
| user_vouchers (other cols) | same | |
| tuitions.* | tuitions.* | term_id NOT NULL |
| point_transactions.type/action/amount/description/user_id/created_at | same | |
| point_transactions.referenceable_type/_id | dropped | always NULL; new user_voucher_id, dedupe_key = NULL |
| point_transactions.updated_at | dropped | append-only |
| career_paths.cover_url | career_paths.cover_path | |
| career_paths.price (int) | career_paths.price numeric(14,2) | |
| career_paths (other cols) | same | |
| career_path_courses.* | same | |
| user_career_paths.* | same | progress_percent tinyint -> smallint |
| certificate_templates.background_image_url | background_image_path | strip storage base URL when it is ours |
| certificate_templates.fields_config | same | NULL -> '[]' |
| user_certificates.* | same | credential_id -> citext |
| job_postings.* | same | required_skills jsonb |
| user_cvs.profile_json / evaluation_json | profile / evaluation | |
| user_cvs.skills | skills | NULL -> '[]' |
| user_cvs (other cols) | same | expected_salary -> numeric(14,2) |
| career_recommendations.ai_summary | analysis jsonb | JSON text -> parsed object; plain text -> `{"overview": text}` |
| career_recommendations.skill_gaps / suggested_courses | same | NULL -> '[]' |
| — | career_recommendations.target_role | new, NULL for migrated rows |

## 4. Open questions

1. `orders.user_id` is `RESTRICT` (legacy CASCADE). Admin hard-deletes users (`AdminController`); the Go port must
   either refuse deletion of users with orders (409) or identity must soft-delete users. Confirm.
2. Other domains should add deferred FKs to this file's tables (they own those files):
   `courses.certificate_template_id -> certificate_templates(id)` and `enrollments.order_id -> orders(id)`
   in `000090_deferred_fks_learning`.
3. Should tuition payment go through `orders`/PayOS (then add `item_type='tuition'` + `tuition_id` to `order_items`)
   or stay admin/demo-marked? `tuitions.order_id` is kept for now.
4. `vouchers.starts_at` is new (requested validity window); the admin UI/API do not send it yet.
5. Should `point_transactions.action` stay a CHECK list (new reward types need a migration) or become free text?
