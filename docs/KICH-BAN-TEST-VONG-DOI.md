# Kịch bản kiểm thử vòng đời: từ dựng học vụ đến học, thi và báo cáo

Kịch bản này đi hết một vòng nghiệp vụ trên giao diện thật, với **3 người dùng ở 3 cửa sổ Chrome, mỗi cửa sổ một profile riêng** (đăng nhập cùng lúc, không đè phiên của nhau):

| Cửa sổ | Người | Tài khoản |
|---|---|---|
| 1 | Quản trị viên (đồng thời đóng vai cố vấn học tập ở bước cuối) | `admin@eript.test`, `advisor@eript.test` |
| 2 | Giảng viên (do quản trị viên tạo mới trong kịch bản) | `gv.<mã>@eript.test` |
| 3 | Sinh viên A (trong lớp) và sinh viên B (ghi danh riêng) | `sv.<mã>a@eript.test`, `sv.<mã>b@eript.test` |

Mọi dữ liệu tạo ra mang một **mã chạy** ngẫu nhiên 5 ký tự (ví dụ `SB70L`) nên chạy nhiều lần không trùng nhau. Mật khẩu của tài khoản demo và tài khoản tạo trong kịch bản đều là `Demo@1234`.

## Cách chạy tự động

Điều kiện: API chạy ở `:8080`, web chạy ở `:3001`, đã nạp dữ liệu demo (có chương trình CNTT, danh mục khóa học, tài khoản cố vấn `advisor@eript.test`), Chrome cài ở `C:\Program Files\Google\Chrome\Application\chrome.exe` (đổi bằng biến `CHROME_PATH`).

```bash
cd eript-web
npm run e2e                                  # 3 cửa sổ Chrome hiện ra, tạo dữ liệu mới nếu chưa có trạng thái cũ
node e2e/full-cycle.mjs --fresh              # luôn tạo dữ liệu mới
node e2e/full-cycle.mjs --headless --fresh   # không hiện cửa sổ (chạy nền / CI)
node e2e/full-cycle.mjs --from=P3            # chạy lại từ pha P3 (dùng lại id đã lưu ở e2e/out/state.json)
node e2e/full-cycle.mjs --keep-going         # lỗi một bước vẫn chạy tiếp các bước sau
node e2e/full-cycle.mjs --keep-open          # giữ các cửa sổ Chrome mở sau khi chạy xong
```

Kết quả nằm ở `eript-web/e2e/out/`: `result.json` (từng bước đạt hay lỗi), ảnh chụp màn hình các bước quan trọng (`exam-result`, `certificate`, `report-detail`, `advisor-class-report`) và ảnh chụp ngay lúc lỗi (`*-FAIL-*.png`). Mỗi bước đã đạt được ghi vào `state.json`, nên khi một pha lỗi, sửa xong chạy lại sẽ bỏ qua các bước đã làm. Lệnh `--redo` ép làm lại.

Mỗi bước vừa thao tác bằng giao diện (điền form, bấm nút) vừa **đối chiếu lại bằng API** để chắc dữ liệu thật sự được lưu đúng, không chỉ "trông có vẻ đúng".

---

## P1. Quản trị viên dựng cấu trúc học vụ và tài khoản (cửa sổ 1)

| # | Thao tác trên giao diện | Kết quả mong đợi |
|---|---|---|
| 1.1 | Vào `/login`, nhập `admin@eript.test` / `Demo@1234`, bấm Đăng nhập | Chuyển vào `/admin` |
| 1.2 | `Quản trị > Học vụ > Năm học` → Thêm năm học: tên `NH-<mã>`, bắt đầu 01/09/2027, kết thúc 31/08/2028 (chọn ngày bằng bộ chọn ngày mới) | Năm học xuất hiện trong danh sách |
| 1.3 | `Học kỳ` → Thêm học kỳ: chọn năm học vừa tạo (ô chọn có tìm kiếm), mã `HK1-<mã>`, tên `Học kỳ 1 NH-<mã>`, 01/09/2027 đến 15/01/2028 | Học kỳ xuất hiện |
| 1.4 | `Khóa` → Thêm khóa: chương trình CNTT, mã `K<mã>`, tên `Khóa <mã>`, năm vào học 2027 | Khóa xuất hiện |
| 1.5 | `Lớp` → Thêm lớp: chương trình CNTT, khóa vừa tạo (danh sách khóa lọc theo chương trình), cố vấn "Cố vấn học tập", mã `L<mã>01` | Lớp xuất hiện; API `/classes?q=` trả đúng 1 lớp |
| 1.6 | `Người dùng` → Thêm người dùng: tên `Giảng viên <mã>`, email `gv.<mã>@eript.test`, **bỏ** vai trò Sinh viên, **chọn** vai trò Giảng viên, mật khẩu `Demo@1234` | Tài khoản xuất hiện; đăng nhập API và gọi `/my-courses` được (có quyền studio) |
| 1.7 | Tạo tiếp 2 sinh viên (vai trò mặc định Sinh viên): `Sinh viên <mã> A`, `Sinh viên <mã> B` | Cả hai xuất hiện trong danh sách |
| 1.8 | Vào `Studio > Lớp quản lý`, mở lớp vừa tạo → Thêm sinh viên: nhập email sinh viên A | Lớp có đúng 1 sinh viên |
| 1.9 | Tab "Chương trình và lộ trình" → Tạo chương trình mới (`CTĐT L<mã>01`) → ở Kỳ 1, chọn "Học kỳ thực tế" = học kỳ vừa tạo | API `/classes/:id` có `term_map` với kỳ 1 |

## P2. Giảng viên tạo khóa học, ngân hàng câu hỏi, kỳ thi (cửa sổ 2)

| # | Thao tác | Kết quả mong đợi |
|---|---|---|
| 2.1 | Đăng nhập bằng tài khoản giảng viên | Vào `/studio` |
| 2.2 | `Khóa học > Tạo khóa học`: tên `Môn thử nghiệm <mã>`, cách học **Học tuần tự**, tick **Cấp chứng chỉ**, bấm Tạo | Chuyển sang trang biên tập; API: `learning_mode = sequential`, có `certificate_template_id` |
| 2.3 | Tab Thông tin: chọn danh mục (ô tìm kiếm), nhập mô tả, Lưu | Khóa có danh mục và mô tả (đủ điều kiện xuất bản) |
| 2.4 | Tab Chương trình: Thêm chương "Chương 1 - Nền tảng"; Thêm bài học ×2 (loại **Bài đọc**, có nội dung) | API `/courses/:id/sections` có đủ 2 bài |
| 2.5 | `Ngân hàng câu hỏi > Tạo ngân hàng` `Ngân hàng <mã>` | Ngân hàng xuất hiện |
| 2.6 | Mở ngân hàng → Nhập từ Excel/Word → chọn `docs/mau-nhap-cau-hoi/mau-nhap-cau-hoi.xlsx` → Kiểm tra → Nhập vào ngân hàng | **10 câu hỏi** và **2 nhóm** tự tạo (Chương 1, Chương 2) |
| 2.7 | `Nhóm câu hỏi`: sửa tên nhóm "Chương 1 - Kinh tế số" thành "Chương 1 - Nền tảng kinh tế số" | Tên mới hiển thị trong danh sách |
| 2.8 | `Kỳ thi > Tạo kỳ thi`: loại **Thi cuối khóa**, chọn khóa học vừa tạo, tên `Thi cuối môn <mã>`, 15 phút | Chuyển sang trang kỳ thi |
| 2.9 | Tab Cài đặt: điểm đạt 50, Lưu thay đổi | API `/exams/:id/detail`: `pass_score = 50` |
| 2.10 | Tab Đề thi → Thêm quy tắc rút → chọn ngân hàng → chế độ **Theo tỉ lệ % độ khó**: tổng 5 câu, Rất dễ 20%, Dễ 60%, Trung bình 20% → Thêm → Lưu đề thi | 3 quy tắc, tổng 5 câu (1 + 3 + 1) |
| 2.11 | Bấm **Mở kỳ thi** | Trạng thái "Đang mở" |
| 2.12 | Tab Xuất bản → Gửi duyệt | Khóa ở trạng thái "chờ quản trị viên duyệt" |

## P3. Duyệt khóa, gán cho lớp, ghi danh thêm (cửa sổ 1 và 2)

| # | Người | Thao tác | Kết quả mong đợi |
|---|---|---|---|
| 3.1 | Quản trị viên | `Quản trị > Khóa học`, mở khóa chờ duyệt → **Duyệt & xuất bản** | API: `status = published` |
| 3.2 | Quản trị viên | `Lớp quản lý` → lớp → Chương trình và lộ trình → ở Kỳ 1 chọn "Thêm môn vào kỳ này" = khóa vừa tạo → **Gán cho cả lớp** → xác nhận | Sinh viên A được ghi danh (1/1), ghi danh gắn đúng **học kỳ của lớp** |
| 3.3 | Giảng viên | Tab Học viên → Thêm học viên: email sinh viên B | Khóa có 2 học viên (A theo lớp, B ghi danh riêng) |

## P4. Sinh viên học, thi, nhận chứng chỉ (cửa sổ 3)

| # | Thao tác | Kết quả mong đợi |
|---|---|---|
| 4.1 | Sinh viên A đăng nhập → `/my` | Thấy thẻ khóa học và kỳ thi sắp tới (đang diễn ra) |
| 4.2 | Mở trang học của khóa | Bài 2 hiện lý do bị khóa "Hãy hoàn thành bài học trước" (học tuần tự) |
| 4.3 | Bài 1: bấm "Đánh dấu hoàn thành" → quay lại | Bài 2 mở khóa. Làm tiếp bài 2 → tiến độ 2/2 (API `/courses/:id/progress`) |
| 4.4 | Mở kỳ thi: trả lời từng câu (trắc nghiệm, nhiều đáp án, điền khuyết, số học, sắp xếp) rồi **Nộp bài** | Đề có đúng 5 câu; trang kết quả hiện điểm |
| 4.5 | Về `/my` → bấm **Nhận chứng chỉ** | Mở `/certificates/<mã>` hiện tên sinh viên và tên khóa học |

## P5. Báo cáo và phân quyền

| # | Người | Thao tác | Kết quả mong đợi |
|---|---|---|---|
| 5.1 | Giảng viên | Tab Báo cáo của khóa | 2 học viên; bảng "Theo lớp" có lớp mới; API: A 100%, có chứng chỉ, có điểm thi; B 0%, chưa thi, chưa có lớp |
| 5.2 | Giảng viên | Bộ lọc "Lọc theo lớp" (chọn nhiều, có ô tìm kiếm) chọn lớp; bấm tên sinh viên A | Chỉ còn A; hộp thoại chi tiết hiện "Bài học (2/2)" và các bài đã hoàn thành |
| 5.3 | Giảng viên | Lọc theo năm học (API) | Chỉ có `NH-<mã>` với 1 học viên (sinh viên được gán qua lớp) |
| 5.4 | Cố vấn học tập (đăng nhập `advisor@eript.test` ở cửa sổ 1) | Tự vào `/studio/classes`; mở lớp → môn → **Báo cáo lớp** | Thấy sinh viên A 100%. API báo cáo khóa **không kèm lớp** hoặc **lớp không phụ trách** đều trả 403 |
| 5.5 | Quản trị viên | Danh sách khóa học, danh sách học viên của khóa | Khóa đã xuất bản, 2 học viên |
| 5.6 | Không đăng nhập | Mở `/certificates/<mã>` | Hiện chứng chỉ công khai; mã sai hiện trang "Không tìm thấy" |

---

## Những gì kịch bản chưa kiểm tra

- Quét **mã QR điểm danh bằng camera thật** và **gửi email thật** (cần phần cứng hoặc SMTP thật).
- Bài tập nộp file và chấm điểm, SCORM, điểm danh GPS: đã kiểm tra riêng ở các lượt trước, chưa nằm trong vòng này.
- Thi có giám sát bằng camera.
- Nhập đề từ Word thật của trường (bảng, ảnh, công thức): cần file đề thật.

## Dữ liệu còn lại sau mỗi lần chạy

Năm học, học kỳ, khóa, lớp, chương trình đào tạo, 3 tài khoản, 1 khóa học đã xuất bản, 1 ngân hàng 10 câu, 1 kỳ thi, 1 chứng chỉ. Tất cả mang mã chạy trong tên nên dễ lọc. Muốn dọn: xóa tài khoản ở `Quản trị > Người dùng`, xóa lớp, khóa, học kỳ, năm học ở `Học vụ` (khóa học đã có ghi danh không xóa được, chỉ gỡ xuất bản).

## Lỗi giao diện đã phát hiện khi viết kịch bản

- Bộ chọn "Học kỳ thực tế" cắt tên học kỳ dài (đã nới ô chọn).
- Trang chứng chỉ với mã sai trả HTTP 200 nhưng hiển thị "Không tìm thấy" (Next stream trang dưới `loading.tsx`). Giống hành vi trang chi tiết khóa học; không ảnh hưởng người dùng, chỉ ảnh hưởng mã trạng thái.
- Một lần chạy đầu ghi nhận cảnh báo hydration ở cửa sổ giảng viên khi Next vừa biên dịch trang; chạy lại không tái hiện. Bộ chạy in cảnh báo trình duyệt theo từng bước (`⚠`) để bắt nếu lặp lại.
