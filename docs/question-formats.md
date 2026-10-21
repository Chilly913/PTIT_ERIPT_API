# Các dạng câu hỏi (ngân hàng câu hỏi)

Mã nguồn: `internal/modules/learning/qtypes.go` (logic từng dạng), `qbank.go` (CRUD), `qbank_files.go` (Excel/Word),
`quiz.go` + `exam_take.go` (làm bài, chấm điểm). Giao diện: `eript-web/src/features/qbank` (soạn câu hỏi), `features/learn/quiz-runner.tsx` (làm bài).

## Danh sách

| `type` | Tên | Chấm điểm | Nhập từ Excel/Word |
|---|---|---|---|
| `single_choice`, `true_false` | Một đáp án, Đúng/Sai | tự động, đúng/sai | có |
| `multiple_choice` | Nhiều đáp án | tự động, phải chọn đúng cả tập | có |
| `short_answer` | Trả lời ngắn | tự động, không phân biệt hoa thường | có |
| `numerical` | Số học | tự động, có sai số (`metadata.tolerance`) | có |
| `ordering` | Sắp xếp | tự động | có |
| `matching` | Ghép đôi | tự động | có |
| `essay` | Tự luận | **chấm tay** (mục "Chấm tay") | có |
| `file_upload` | Nộp tệp | **chấm tay** | có (chỉ nội dung câu hỏi) |
| `cloze` | Điền khuyết | tự động, **cho điểm từng phần** | có |
| `classification` | Phân loại (kéo thả vào nhóm) | tự động, **cho điểm từng phần** | có |
| `passage` | Đoạn văn + câu hỏi con | điểm = tổng điểm các câu con | không (tạo trên giao diện, JSON import/export có) |

## Cấu trúc dữ liệu của các dạng mới

Dùng chung bảng `questions` và `question_answers`, cấu hình riêng nằm trong `questions.metadata` (jsonb).

### cloze

- `content`: văn bản có chỗ trống `{{1}}`, `{{2}}`... (tối đa 20 ô, số từ 0 đến 99).
- `question_answers`: mỗi dòng một văn bản; `sub_content` = số ô ("1"), `content` = văn bản, `is_correct` = đúng/sai.
  Nhiều dòng đúng cùng một ô = nhiều cách viết được chấp nhận.
- Ô nào có **phương án sai** (`is_correct = false`) thì hiện dạng danh sách chọn hoặc kéo thả (các phương án bị xáo trộn); ô không có thì là ô nhập chữ.
- `metadata`: `partial_credit` (mặc định `true`), `case_sensitive` (mặc định `false`), `input` = `select` | `drag`.
- Đáp án của người học: `{"1": "Hà Nội", "2": "Hồng"}`. So sánh sau khi bỏ khoảng trắng thừa.

### classification

- `question_answers`: `content` = mục cần xếp, `sub_content` = nhóm đúng. Cần ít nhất 2 mục và 2 nhóm; nhiều mục có thể cùng nhóm.
- `metadata.partial_credit` (mặc định `true`).
- Đáp án của người học: `{"<id mục>": "<tên nhóm>"}` (cùng định dạng với `matching`). Người học thấy danh sách mục (đã xáo) và danh sách nhóm, không thấy nhóm đúng của từng mục.

### passage

- `content` = đoạn đọc, không có đáp án, `default_score` luôn là 0.
- Câu hỏi con là `questions` thường (mọi dạng trừ `passage`) có `parent_question_id` trỏ tới đoạn văn, cùng ngân hàng. Xóa đoạn văn thì xóa các câu con.
- Tạo câu con: `POST .../questions` với `parent_question_id`. Không đổi được cha sau khi tạo; không lồng đoạn văn.
- Danh sách ngân hàng chỉ trả câu cấp cao; câu con nằm trong `children` của đoạn văn. `question_count` của ngân hàng không tính chính đoạn văn.
- Lập đề (bài kiểm tra nhanh, kỳ thi): chọn **đoạn văn**, không chọn riêng câu con (API trả 422). Khi làm bài, đoạn văn và các câu con được trải ra liên tiếp (`expandPassages`); câu con có `parent_id`.
  Rút ngẫu nhiên theo quy tắc chỉ rút câu cấp cao, mỗi đoạn văn tính là 1 mục; điểm lấy từ các câu con.

### Chấm điểm từng phần

`gradeResult.Fraction` (0..1) nhân với điểm của câu. Kết quả xem lại có `is_correct`, `partial` (đúng một phần), `score`. Các dạng cũ chỉ trả 0 hoặc 1 nên kết quả không đổi.

## Excel / Word

Mỗi cột "Đáp án A..F" là một ô, cột "Đáp án đúng" để trống với dạng mới.
- Phân loại: `mục | tên nhóm` (như ghép đôi).
- Điền khuyết: `số ô: đáp án | đáp án khác | ~phương án sai`, ví dụ `2: Hồng | ~Mê Kông`.
- Tối đa 6 ô đáp án mỗi dòng. Xuất Excel/Word bỏ qua đoạn văn và câu con (chúng chỉ có trong JSON).
- Xuất/nhập JSON (`format=json`) giữ nguyên cây đoạn văn – câu con.

## Thêm một dạng câu hỏi mới

1. Migration: mở rộng `ck_questions_type` (xem `000095_question_formats`).
2. `qtypes.go`: viết một kiểu cài `questionType` (`check`, `present`, `grade`) và đăng ký trong `questionTypes`. Viết test trong `qtypes_test.go`.
3. Nếu nhập được từ file: thêm nhãn trong `typeLabels`, nhánh trong `buildQuestion`, `excelRow`, `docxParagraphs` (`qbank_files.go`); không thì thêm vào `notInFiles`.
4. Web: `qbank/types.ts` (`QType`), `qbank/messages.ts` (nhãn), `qbank/question-editor.tsx` (soạn), `learn/quiz-runner.tsx` (làm bài + xem lại).

## Chấm tay (tự luận, nộp tệp)

Bài làm có câu `essay` hoặc `file_upload` được đóng với `quiz_attempts.grading_status = 'pending'`: điểm hiện tại chỉ gồm các câu tự chấm, `passed` chưa có giá trị cuối.
Người chấm (admin, giảng viên của khóa học, hoặc người tạo kỳ thi) chấm trong Studio > **Chấm bài** (`/studio/grading`).

| API | Việc |
|---|---|
| `GET /grading/attempts?status=pending\|graded\|all&q=&course_id=&exam_id=` | danh sách bài chờ chấm; chỉ trả bài thuộc quyền của người gọi |
| `GET /grading/attempts/:id` | từng câu: câu trả lời, tệp đã nộp, điểm tự chấm, điểm đã chấm tay |
| `PUT /grading/attempts/:id` | `{"questions": {"<id câu>": {"score": 3, "comment": "..."}}, "overall_feedback": "..."}` |

- Điểm mỗi câu từ 0 đến điểm của câu (422 nếu vượt); chỉ chấm được câu `essay`/`file_upload`. Có thể chấm từng phần: bài vẫn `pending` cho tới khi mọi câu đều có điểm, lúc đó thành `graded`.
- Mỗi lần lưu, điểm bài được tính lại từ câu trả lời đã lưu cộng điểm chấm tay (`scoreAttempt`, dùng chung với lúc nộp bài), cập nhật `score`, `passed`. Bài kiểm tra nhanh đạt thì tính hoàn thành bài học như khi tự chấm.
- Người không có quyền nhận 404 (không lộ bài làm có tồn tại).

### Nộp tệp

- `metadata`: `max_files` (1-5, mặc định 1), `max_size_mb` (1-25, mặc định 10), `allowed_ext` (mặc định pdf, office, zip, ảnh, txt).
- `POST /attempts/:id/files` (multipart `question_id`, `file`), `DELETE /attempts/:id/files/:fid`, `GET /attempts/:id/files/:fid` (chủ bài hoặc người chấm). Chỉ khi bài đang làm dở.
- Tệp lưu ở `UPLOAD_DIR/private/answers/<attempt_id>/`, không công khai. Khi nộp bài, câu trả lời được dựng lại đúng theo các tệp đã tải lên (không tin id tệp client gửi).
- Hạn chế: xóa bài làm/bài học bằng cascade xóa dòng `attempt_files` nhưng **không xóa tệp trên đĩa**; cần dọn định kỳ thư mục `private/answers` của các bài không còn tồn tại.

### Người học xem kết quả

- `GET /my/attempts?lesson_id=` hoặc `?exam_id=`: các lần làm đã nộp của người gọi. Trang bài kiểm tra nhanh hiện "Các lần làm bài" dưới nút làm bài.
- `GET /attempts/:id/result` (chỉ chủ bài làm, người khác 404): điểm, từng câu, điểm và nhận xét của người chấm, nhận xét chung. Với kỳ thi áp dụng `review_options` của kỳ thi (điểm, đáp án, giải thích có được xem hay không). Trang web: `/attempts/:id`.
- Khi bài **chấm xong hoàn toàn** (lần đầu chuyển sang `graded`), người học nhận thông báo "Bài làm đã được chấm" trỏ tới `/attempts/:id`. Chấm lại không gửi thông báo thứ hai.

## Chưa làm

- Bài làm đã nộp trước migration `000096` mà có câu tự luận không được đánh dấu chờ chấm.
- Kéo thả bằng chuột đã cài (HTML5 drag and drop) nhưng mới kiểm tra tự động cách bấm chọn rồi đặt; cần thử tay trên trình duyệt và màn hình cảm ứng. Ô tải tệp của người học chưa thử trên giao diện (đã thử qua API).
- Lập đề: ô "điểm riêng" của một đoạn văn trong đề không có tác dụng (điểm lấy từ câu con).
