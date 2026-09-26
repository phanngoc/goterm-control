# architect/

Tài liệu kiến trúc: **hệ thống này được thiết kế ra sao và vì sao**.

Khác với `docs/`, nơi mô tả cách **dùng** nó, và khác với `docs/design/`, nơi giữ các bản đề
xuất theo từng tính năng.

| | |
|---|---|
| [`skill-learning.md`](skill-learning.md) | Hệ kĩ năng tự học: mô hình, ràng buộc, bốn bước, nhật ký quyết định |
| [`architecture-review.vi.md`](architecture-review.vi.md) | Đánh giá kiến trúc toàn hệ (13/09/2026) |

## Một tài liệu ở đây nên có

**Nhật ký quyết định kèm cái mất.** Một quyết định ghi lại mà không nói nó đánh đổi gì thì
người đọc sau không biết khi nào được phép đổi ý.

**Ràng buộc trước giải pháp.** Phần lớn kiến trúc ở đây bị quyết bởi những thứ không chọn được —
ba backend là CLI của người khác, không có chỗ đặt guard, một binary dùng chung cho ba agent.

**Trạng thái thật.** Cái gì đã chạy, cái gì chưa, và **chỗ nào còn chưa nối**. Một tài liệu mô
tả đích đến như thể đã tới là tài liệu khiến người ta đi tìm thứ không tồn tại.
