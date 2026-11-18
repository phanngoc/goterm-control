# game-iterate — vòng sửa

## Nguyên tắc

1. **Patch, không rebuild.** Đọc code hiện tại, sửa tối thiểu. Sinh lại từ spec làm mất mọi cân bằng
   đã tinh chỉnh bằng tay.
2. **Phản hồi cảm tính → tham số cụ thể.** "Địch nhanh quá" → đổi hằng số `speed` nào, từ bao nhiêu sang
   bao nhiêu. Nói rõ con số cho người dùng, đừng nói "đã giảm tốc độ".
3. **Cập nhật `spec.json` cùng lúc.** Spec lệch code thì `stages/playtest.md` sinh test sai. Nếu thay đổi phá vỡ
   một ràng buộc trong `spec.balance` → sửa ràng buộc đó, đừng lặng lẽ vi phạm.
4. **Chạy lại `stages/playtest.md`** sau mỗi patch. Sửa cân bằng rất dễ làm game không thể thắng được nữa.
5. **Commit từng patch riêng** với thông điệp nói rõ số cũ → số mới. Đây là thứ giúp rollback được khi
   một thay đổi "nghe hợp lý" làm game dở đi.

## Ba loại yêu cầu và cách xử

| Yêu cầu | Làm gì |
|---|---|
| Cân bằng ("dễ quá", "nhanh quá") | Đổi hằng số ở đầu file, chạy lại G3 (bot phải vẫn thắng được) |
| Nội dung ("thêm màn", "thêm loại địch") | Thêm vào bảng dữ liệu, không thêm nhánh `if` trong logic |
| Hiệu năng ("giật trên điện thoại") | Đo trước khi sửa: cấp phát trong vòng lặp render, DPR, số draw call. Bài học tank-battle: tách canvas layer + bỏ cấp phát mỗi frame |

## Không làm

Không thêm tính năng người dùng không yêu cầu. Game nhỏ hỏng vì phình scope nhanh hơn vì thiếu tính năng.
