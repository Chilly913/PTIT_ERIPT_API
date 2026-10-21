# Khảo sát loại học liệu: ERIPT hiện có và so sánh với Moodle

Mục đích: chọn loại học liệu nên làm tiếp, ưu tiên loại có thể dựng nhanh nhờ API hoặc thư viện có sẵn.
Cập nhật: 19/10/2026. Phần "Hiện có" lấy từ mã nguồn trong repo; phần Moodle lấy từ tài liệu chính thức (nguồn ở cuối).
Các thư viện và API ở mục 4 là đề xuất dựa trên hiểu biết hiện tại, cần thử nhanh (spike) trước khi chốt.

## 1. Tóm tắt và khuyến nghị

- ERIPT đã thiết kế **16 loại bài học** và **8 dạng câu hỏi**. Giao diện học viên hiển thị được 14 loại, **chưa hiển thị forum và survey**.
- **Backend Go chưa có API nào cho học liệu** (chỉ có lược đồ CSDL). Vì vậy mọi loại bên dưới đều cần viết API trước khi chạy thật.
- So với Moodle (16 hoạt động + 7 tài nguyên), ERIPT đã phủ phần giảng dạy cốt lõi (video, trang, tệp, SCORM, H5P, bài tập, trắc nghiệm, lớp trực tuyến). Còn thiếu chủ yếu nhóm **tương tác và cộng tác**: Forum, Feedback, Choice, Wiki, Glossary, Workshop, Chat, LTI.
- Đề xuất làm theo 3 đợt (mục 5). Ba loại **nên làm trước vì rẻ mà giá trị cao**:
  1. **Nhúng ngoài + PDF + URL** (Google Slides/Drive, YouTube, PDF.js): công sức nhỏ, dùng được ngay.
  2. **H5P** (thư viện `h5p-standalone`): lược đồ đã có sẵn (`scorm_packages.version = 'h5p'`), tạo nội dung bằng Lumi hoặc h5p.com.
  3. **Lớp trực tuyến bằng Jitsi** (iframe API, không cần khóa API): thay cho việc chỉ dán đường dẫn Zoom/Meet.

## 2. Hiện có trong ERIPT

| Loại bài học | CSDL | Trình soạn khóa học | Trình học viên | API Go | Ghi chú |
|---|---|---|---|---|---|
| `video` | có | có | có | chưa | Nhúng YouTube, Vimeo hoặc tệp trực tiếp; theo dõi thời gian xem, tự hoàn thành. `video_status` (pending/processing/ready/failed) đã có nhưng **chưa có bước chuyển mã** |
| `audio` | có | có | có | chưa | Dùng chung trình phát với video |
| `file`, `document` | có | có | có | chưa | Thẻ tải xuống; bảng `lesson_attachments` cho tệp đính kèm |
| `page` | có | có | có | chưa | HTML (soạn bằng trình soạn thảo có sẵn), có lọc an toàn |
| `quiz` | có | có | có | chưa | Ngân hàng câu hỏi + đề; làm bài có đồng hồ, tự lưu, xem lại |
| `assignment` | có | có | có | chưa | Hướng dẫn, đuôi tệp cho phép, hạn nộp, chấm điểm |
| `scorm`, `h5p` | có | có | có | chưa | `scorm_packages` (1.2, 2004, h5p) + `scorm_runtime_states` |
| `virtual_class`, `zoom`, `meet`, `live` | có | có | có | chưa | Chỉ là thẻ đường dẫn tham gia + giờ bắt đầu, **chưa tích hợp API** |
| `offline` | có | có | có | chưa | Buổi học trực tiếp, điểm danh QR |
| `forum`, `survey` | có (trong CHECK) | có thể chọn | **chưa hiển thị** (báo "chưa hỗ trợ") | chưa | Chỉ mới có tên loại |

Dạng câu hỏi hiện có (8): một đáp án, nhiều đáp án, đúng/sai, tự luận, ghép đôi, sắp xếp, trả lời ngắn, số học.
Tính năng đi kèm: ghi chú theo thời điểm video, hỏi đáp theo khóa, đánh giá khóa, tiến độ, chứng nhận, thi có giám sát.
Phía Python (`PTIT_ERIPT_AI`) đã có sẵn: sinh tên khóa, mô tả bài, đề quiz, đề thi; phân tích kết quả thi; hỏi đáp RAG; gợi ý học.

## 3. So sánh với Moodle

Moodle 4.5/5.0 gồm 16 hoạt động và 7 tài nguyên.

| Moodle | ERIPT | Mức độ | Cách làm gợi ý |
|---|---|---|---|
| **Tài nguyên** | | | |
| File | `file`, `document` | Có | Thêm xem trước PDF/Office |
| Page | `page` | Có | |
| URL | (đang gộp trong video/document) | Một phần | Tách loại "Liên kết" riêng |
| Text and media area (Label) | — | Thiếu | Khối chữ/ảnh chèn giữa các bài, rẻ |
| Folder | — | Thiếu | Nhóm nhiều tệp (dùng `lesson_attachments`) |
| Book | — | Thiếu | Nhóm nhiều trang có mục lục |
| IMS content package | — | Thiếu | Ít cần; SCORM/H5P đã phủ phần lớn |
| **Hoạt động** | | | |
| Assignment | `assignment` | Có | |
| Quiz | `quiz` | Có | Moodle còn có thêm cloze, tính toán, kéo thả vào văn bản |
| SCORM | `scorm` | Có | |
| H5P | `h5p` | Có (chưa có API) | Xem mục 4 |
| BigBlueButton | `virtual_class` | Một phần | Tạo/tham gia phòng qua API |
| Forum | `forum` (chỉ tên) | Chưa dựng | Tái dùng khung hỏi đáp `course_qas` |
| Survey | `survey` (chỉ tên) | Chưa dựng | |
| Feedback | — | Thiếu | Trình dựng biểu mẫu khảo sát |
| Choice | — | Thiếu | Bình chọn nhanh, rất nhỏ |
| Lesson (rẽ nhánh) | — | Thiếu | Ưu tiên thấp |
| Workshop (chấm chéo) | — | Thiếu | Lớn, đợt sau |
| Wiki | — | Thiếu | Soạn thảo cộng tác |
| Glossary | — | Thiếu | Thuật ngữ theo khóa, nhỏ |
| Database | — | Thiếu | Ít dùng |
| Chat | — | Thiếu | Có thể thay bằng chatbot/forum |
| LTI (External tool) | — | Thiếu | Chiến lược: mở cổng dùng học liệu bên thứ ba |

ERIPT có mà Moodle lõi không có sẵn: trợ lý AI và sinh nội dung/đề (Python), điểm danh QR, thi có giám sát bằng khuôn mặt.

## 4. API và thư viện có thể giúp làm nhanh

| Mục tiêu | Lựa chọn | Độ khó | Ghi chú |
|---|---|---|---|
| Nhúng Google Slides/Docs/Drive/Forms, YouTube | Đường dẫn nhúng chính thức, `youtube-nocookie` | Nhỏ | Đã có YouTube/Vimeo; chỉ cần mở rộng bộ nhận diện URL |
| Xem PDF trong trang | PDF.js | Nhỏ | Không cần máy chủ |
| H5P | `h5p-standalone` (hiển thị gói `.h5p` đã giải nén) | Vừa | Tác giả soạn bằng Lumi hoặc h5p.com rồi tải gói lên |
| SCORM runtime | `scorm-again` (cầu nối API SCORM 1.2/2004 phía trình duyệt) | Vừa | Ghi trạng thái vào `scorm_runtime_states` |
| Lớp trực tuyến | **Jitsi** iframe API | Nhỏ | Không cần khóa; tự lưu tên phòng |
| | BigBlueButton API (tạo/tham gia phòng, ký bằng checksum) | Vừa | Cần máy chủ BBB |
| | Zoom/Google Meet tạo cuộc họp qua API | Lớn | Cần OAuth và duyệt ứng dụng; giữ dán đường dẫn là đủ cho giai đoạn đầu |
| Video tải lên | Dịch vụ phát video (Cloudflare Stream, Bunny, Mux) hoặc ffmpeg tạo HLS | Vừa đến lớn | Dịch vụ ngoài tiết kiệm công vận hành hơn tự chuyển mã |
| Biểu mẫu khảo sát (Feedback, Survey) | SurveyJS hoặc tự dựng bằng bảng câu hỏi hiện có | Vừa | |
| Forum | Tái dùng `course_qas` + trả lời lồng nhau | Vừa | |
| Wiki | Tiptap (đã dùng) + Yjs | Vừa đến lớn | |
| LTI 1.3 | Thư viện LTI cho Go/Node | Lớn | Mở nhiều học liệu bên thứ ba |
| Sinh học liệu bằng AI | Python service sẵn có | Nhỏ | Mở rộng sinh đề, tóm tắt bài, thẻ ghi nhớ |

## 5. Lộ trình đề xuất

**Đợt 1 – rẻ, thấy ngay (ưu tiên):**
Viết API học liệu cơ bản (khóa học, mục, bài, tệp đính kèm, tiến độ) rồi thêm: nhúng ngoài mở rộng, PDF.js, loại "Liên kết", khối chữ/ảnh (Label), thư mục tệp, **H5P**, **Jitsi**. Dùng `POST /api/uploads` sẵn có cho tệp (mở rộng loại tệp cho học liệu).

**Đợt 2 – tương tác:**
SCORM runtime, Forum (tái dùng hỏi đáp), Choice, Feedback/Survey, Book, tải video lên (chọn dịch vụ phát video trước).

**Đợt 3 – nâng cao:**
BigBlueButton, Workshop, Wiki, Glossary, LTI 1.3, thêm dạng câu hỏi (cloze, tính toán, kéo thả).

## 6. Cần quyết định

1. Có làm tiếp phần API cho khóa học/bài học (đang trống) trước khi thêm loại mới không. Đây là điều kiện để mọi loại học liệu chạy thật.
2. Video tải lên: dùng dịch vụ ngoài hay tự chuyển mã.
3. Lớp trực tuyến: Jitsi (nhanh, miễn phí) hay BigBlueButton (đầy đủ hơn, cần máy chủ).
4. Nội dung H5P: tác giả tự soạn bằng Lumi/h5p.com rồi tải lên, hay cần trình soạn tích hợp (rất lớn).

## Nguồn

- [Moodle – Activities (5.0)](https://docs.moodle.org/500/en/Activities)
- [Moodle – Resources (5.0)](https://docs.moodle.org/500/en/Resources)
- [Moodle – Activity chooser](https://docs.moodle.org/502/en/Activity_chooser)
- Mã nguồn ERIPT: `migrations/000003_learning.up.sql`, `eript-web/src/features/learn/lesson-view.tsx`, `eript-web/src/features/qbank/types.ts`, `PTIT_ERIPT_AI/routers/`
