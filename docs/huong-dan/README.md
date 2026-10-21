# Hướng dẫn sử dụng ERIPT LMS

Bộ hướng dẫn chia theo **vai trò**. Mỗi người chỉ cần đọc file của mình.

| Bạn là | Đọc file | Bạn dùng hệ thống để |
|---|---|---|
| Ai cũng cần đọc trước | [00-dang-nhap-va-tai-khoan.md](00-dang-nhap-va-tai-khoan.md) | Đăng ký, đăng nhập, quên mật khẩu, hiểu các vai trò |
| Sinh viên / học viên | [01-sinh-vien.md](01-sinh-vien.md) | Học khóa học, làm bài tập, thi, điểm danh, nhận chứng chỉ |
| Giảng viên | [02-giang-vien.md](02-giang-vien.md) | Tạo khóa học, ngân hàng câu hỏi, kỳ thi, chấm bài, xem báo cáo |
| Cố vấn học tập | [03-co-van-hoc-tap.md](03-co-van-hoc-tap.md) | Quản lý lớp, sinh viên, chương trình đào tạo, gán môn cho lớp |
| Giáo vụ (quản lý đào tạo) | [04-giao-vu.md](04-giao-vu.md) | Dựng năm học, học kỳ, khóa, lớp hành chính |
| Quản trị viên | [05-quan-tri-vien.md](05-quan-tri-vien.md) | Người dùng, phân quyền, duyệt khóa học, trang chủ, cài đặt |

Cán bộ **tài chính** có tài khoản trong hệ thống nhưng phiên bản này chưa có màn hình riêng (thanh toán học phí sẽ làm sau), nên chưa có hướng dẫn.

## Tài khoản demo để thực hành

Dữ liệu demo đã có sẵn. Mật khẩu chung của mọi tài khoản demo: `Demo@1234` (đổi hoặc xóa trước khi dùng thật).

| Vai trò | Email | Vào đâu sau khi đăng nhập |
|---|---|---|
| Sinh viên | `student@eript.test` | Việc học của tôi (`/my`) |
| Giảng viên | `instructor@eript.test` | Studio (`/studio`) |
| Cố vấn học tập | `advisor@eript.test` | Lớp quản lý (`/studio/classes`) |
| Giáo vụ | `academic@eript.test` | Học vụ (`/admin/academic/classes`) |
| Quản trị viên | `admin@eript.test` | Quản trị (`/admin`) |

Lớp demo **D24CNTT01** có 4 sinh viên đã ghi danh khóa "Nhập môn Kinh tế số" với tiến độ khác nhau, nên các màn báo cáo có số liệu để xem.

## Quy ước trong tài liệu

- **In đậm** là tên nút, tab hoặc mục menu đúng như trên màn hình.
- `Menu > Mục con` là đường đi trên thanh menu bên trái.
- Ảnh minh họa nằm ở thư mục [img/](img/); chụp từ dữ liệu demo nên tên và số liệu của bạn sẽ khác.
- Kịch bản kiểm thử toàn bộ vòng đời (dựng học vụ, tạo khóa, học, thi, báo cáo) nằm ở [../KICH-BAN-TEST-VONG-DOI.md](../KICH-BAN-TEST-VONG-DOI.md).

## Chạy lại ảnh minh họa

Khi giao diện đổi, chụp lại toàn bộ ảnh bằng:

```bash
cd eript-web
node e2e/screenshots.mjs            # tất cả vai trò
node e2e/screenshots.mjs giang-vien # chỉ một vai trò: chung | sinh-vien | giang-vien | co-van | giao-vu | quan-tri
```
