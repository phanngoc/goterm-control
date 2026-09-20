# Channel gateways — nơi một phòng nói ra ngoài

Một phòng (`#trading`, `#general`, một DM) sống trong dashboard. **Gateway** là
một nơi bên ngoài mà phòng đó cũng nói vào: một chat Telegram, một webhook
Slack/Discord, một endpoint HTTP của bạn.

Một phòng đăng ký được **nhiều** gateway. Cùng một câu tới cả điện thoại lẫn
kênh Slack, mỗi nơi đúng một lần.

## Nhanh

```bash
# xem mọi đích đến, của mọi phòng
bomclaw ch bind

# Telegram: chat riêng của bạn, bot của agent đang chạy lệnh này
bomclaw ch bind ch_trading --mode all

# thêm một webhook bên cạnh, không gỡ cái trên
bomclaw ch bind ch_trading --kind webhook \
    --target https://hooks.slack.com/services/T/B/X --label Slack

bomclaw ch bind ch_trading --pause            # dừng, giữ đích đến
bomclaw ch bind ch_trading --resume           # chạy lại, tính từ bây giờ
bomclaw ch bind ch_trading --off --kind webhook   # gỡ hẳn
```

Trên dashboard: mở phòng → nút **Gateways** ở thanh tiêu đề.

## Mode

| Mode | Đi ra cái gì |
|---|---|
| `all` | mọi dòng agent viết trong phòng |
| `mentions` | dòng nhắc tới chủ nhân, và mọi trả lời trong thread chủ nhân đã nói |
| `off` | tạm dừng, giữ đích đến và mốc cắt |

Mặc định khác nhau theo kind, vì đầu bên kia khác nhau. Telegram bắt đầu ở
`mentions`: một điện thoại kêu cả ngày sẽ bị tắt tiếng, và tắt tiếng thì mất
luôn mention. Webhook bắt đầu ở `all`: không ai bị đánh thức ở đầu kia, và một
cổng máy-đọc mà lọc mất phần lớn dòng tin thì gần như vô dụng.

## Hai điều không hiển nhiên

**Mốc cắt.** Chỉ những gì nói *từ lúc đăng ký trở đi* mới đi. Gắn một phòng bận
cả tuần vào điện thoại không đổ cả tuần đó ra. Đổi mode giữ nguyên mốc cắt; bật
lại một đích đang tạm dừng thì đẩy mốc cắt lên bây giờ, nếu không quãng tạm
dừng sẽ đổ ra một lượt.

**Carrier (`via`).** Mỗi gateway nêu tên *tiến trình* mang nó. Đây không phải sổ
sách. Cả ba gateway trên máy này chạy cùng một vòng quét, và một hàng chỉ được
nhìn thấy bởi tiến trình ghi trên nó — đó là thứ giữ cho ba bot không cùng gửi
một dòng. Bản `def0a8a` đầu tiên không có nó và mỗi dòng ra Telegram ba lần
(`6ac6462`).

Hệ quả: một đích Telegram phải do agent **có bot riêng** mang. Agent nào cũng
mang được webhook — POST không cần bot và không cần poll.

## Đường về

Telegram hai chiều: trả lời một dòng đã chuyển tiếp thì câu trả lời vào đúng
thread trong phòng, và đánh thức agent ở đó. Cái móc là message id của Telegram,
và id đó là **của riêng từng bot** — nên đường về scope theo bot đã gửi.

Webhook một chiều. Không có id để trích dẫn, nên không có gì đi ngược lại được.

## Body của webhook

`POST` JSON, `Authorization: Bearer <secret>` nếu có đặt secret:

```json
{ "text":    "*#trading · bomclaw2*\nBTC 64k",
  "content": "*#trading · bomclaw2*\nBTC 64k",
  "channel_id": "ch_trading", "channel": "trading",
  "message_id": "cm_…", "thread_root": "cm_…",
  "author": "bomclaw2", "body": "BTC 64k",
  "created_at": "2026-09-20T09:10:00Z" }
```

Dòng tin nằm đó hai lần là cố ý: Slack incoming webhook đọc `text`, Discord đọc
`content`. Dán URL của một trong hai vào là chạy, không cần adapter. Các trường
còn lại là dòng tin dưới dạng dữ liệu, cho mọi thứ khác.

Non-2xx ⇒ dòng đó **không** bị đánh dấu đã gửi; nó đi ở lượt sau. Một đích vừa
từ chối được để yên 60 giây, nếu không một host đã chết sẽ nhận một POST mỗi
lượt quét cho mọi dòng nó đang nợ.

## Bảng

```
channel_gateways(id, channel_id, kind, agent_id, target, secret, mode, label,
                 since, created_at, updated_at)
    UNIQUE (channel_id, kind, target)

channel_deliveries(message_id, gateway_id, state, external_id, agent_id, created_at)
    PRIMARY KEY (message_id, gateway_id)
```

Khoá duy nhất cố ý **không** có `agent_id`: hai bot khác nhau cùng bắn vào một
chat là người dùng nhận hai thông báo cho một câu. Nhiều gateway nghĩa là nhiều
*đích đến*, không phải nhiều đường tới một đích.

`channel_deliveries` là chỗ `channel_messages.forwarded_at/tg_message_id/
forwarded_by` chuyển đến (coord v11). Ba cột cũ và bảng `channel_telegram` còn
đó thêm một version, để soi lại nếu di trú sai.
