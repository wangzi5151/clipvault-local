# ClipVault-Local IPC 协议（Go sidecar ↔ Tauri Rust 外壳 ↔ 前端）

Go 程序 `clipvault-core` 作为 Tauri sidecar 运行，通过 stdin/stdout 做
**换行分隔的 JSON-RPC 2.0** 通信。每一行是一个完整 JSON。

## 方向

### 请求（Tauri → Go，Go 回包）

```json
{"jsonrpc":"2.0","id":7,"method":"list","params":{"limit":50}}
{"jsonrpc":"2.0","id":7,"result":{...}}
{"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"..."}}
```

`id` 为数字，自增。`params` 缺省时可省略。

### 事件（Go → Tauri，单向推送，无 id）

```json
{"jsonrpc":"2.0","event":"entry_added","data":{"id":123,"kind":"text"}}
```

Rust 外壳收到事件后 `app.emit("core-event", payload)` 转发给前端；
前端 `listen("core-event", ...)` 接收。

## 方法清单

| method | params | result |
|---|---|---|
| `ping` | — | `{version:"0.1.0"}` |
| `get_state` | — | `{recording:bool, entry_count:int, text_count:int, image_count:int, image_bytes:int, db_path:string, version:string}` |
| `set_recording` | `{enabled:bool}` | `{recording:bool}` |
| `list` | `{limit:int=50, offset:int=0, query:string="", tag:string="", kind:string="" , favorites_only:bool=false}` | `{entries:[EntrySummary], total:int}` |
| `get` | `{id:int}` | `EntryDetail` |
| `copy` | `{id:int}` | `{ok:true}`（把该条目重新写入系统剪贴板） |
| `set_favorite` | `{id:int, favorite:bool}` | `{ok:true}` |
| `add_tag` | `{id:int, tag:string}` | `{tags:[string]}` |
| `remove_tag` | `{id:int, tag:string}` | `{tags:[string]}` |
| `list_tags` | — | `{tags:[string]}`（按使用频率排序） |
| `delete` | `{id:int}` | `{ok:true}` |
| `clear_all` | — | `{deleted:int}` |
| `clear_recent` | `{minutes:int}` | `{deleted:int}` |
| `get_settings` | — | `Settings` |
| `set_settings` | `{patch: object}` | `Settings`（返回合并后的完整设置；非法值返回 error） |
| `export_json` | `{dir:string}` | `{dir:string, files:int}`（写出 entries.json + images/） |
| `export_backup` | `{path:string}` | `{path:string, bytes:int}`（单文件 .cvbak = zip：db 拷贝 + meta.json） |
| `import_backup` | `{path:string}` | `{imported:int, skipped:int}`（只认自己导出的 .cvbak；格式不对返回 error；导入前自动先备份当前库） |
| `auto_backup_now` | — | `{path:string}` |
| `save_image` | `{id:int, path:string}` | `{path:string}`（把原图另存到用户选的路径） |
| `resolve_sensitive` | `{token:string, record:bool}` | `{ok:true}` |

### EntrySummary

```json
{"id":123,"kind":"text","preview":"前40字…","created_at":1728,"favorite":false,
 "tags":["订单"],"has_image":false,"text_len":120,"sensitive":false}
```

`created_at` 为毫秒时间戳。`preview` 为纯文本前 60 字（图片条目为"图片 · 800×600"）。

### EntryDetail

在 Summary 基础上加：`text`（完整文本，图片条目为 ""）、
`image_thumb`（base64 PNG 缩略图，最长边 ≤320）、
`image_full`（base64 原图；图片条目点开预览时才请求）、
`image_w`、`image_h`。

### Settings（全部有默认值，set_settings 只传要改的）

```json
{
  "recording": true,
  "max_entries": 200,
  "record_images": true,
  "image_space_mb": 200,
  "record_text": true,
  "hotkey": "Shift+Super+V",
  "theme": "auto",
  "autostart": false,
  "skip_sensitive": "ask",
  "db_dir": "",
  "auto_backup": true,
  "backup_interval_days": 7,
  "lock_action": "none",
  "db_password_set": false,
  "onboarded": false
}
```

- `hotkey`: 前端录制，如 `"Shift+Super+V"`；空字符串 = 关闭全局热键。
  Rust 侧把 `Super` 映射为平台键（Win/Cmd）。
- `skip_sensitive`: `"ask"` 弹出确认 / `"auto"` 自动跳过 / `"off"` 照常记录。
- `lock_action`: `"none"` / `"pause"`（锁屏暂停记录） / `"clear"`（锁屏清空最近1小时）。
- 设置密码走专用方法，不走 set_settings（避免日志残留）：
  - `set_db_password {password}` → 加密全库；`{password:""}` → 解密。
  - 加密算法：scrypt(N=32768,r=8,p=1) 派生 32 字节，AES-256-GCM，
    每值随机 12 字节 nonce，存 `nonce‖ciphertext`。

## 事件清单

| event | data |
|---|---|
| `entry_added` | `{id, kind}` |
| `entry_deleted` | `{id}` |
| `entries_cleared` | `{deleted}` |
| `state_changed` | `{recording}` |
| `settings_changed` | `{}`（前端重拉 get_settings） |
| `sensitive_prompt` | `{token, preview, kind}`（kind=text/image；60 秒内需 resolve_sensitive，超时按 skip_sensitive=auto 处理为跳过） |
| `backup_done` | `{path}` |
| `core_error` | `{message}` |

## 捕获规则（Go 侧必须实现）

1. 只监听剪贴板文本与图片；**不碰文件拖放**（文件剪贴直接忽略，避免爆库）。
2. 文本 >1MB、图片解码后 >25MB 直接忽略。
3. 连续相同内容（sha256 相同）不重复入库。
4. 敏感词检测（`skip_sensitive != "off"` 时）：文本含 `密码/口令/验证码/支付/银行/信用卡/token/passwd/pwd`（大小写不敏感）即触发。
   图片不做 OCR（明确不做），图片只看 `skip_sensitive=="auto"` 时是否跳过——图片默认照常记录。
5. 条数上限：超 `max_entries` 时删除最旧的非收藏条目；收藏永不自动删除。
6. 图片空间上限：图片总字节超 `image_space_mb` 时删除最旧的非收藏**图片**条目。
7. 富文本：Windows 下尽量同时抓取 `HTML Format` 存入 `html` 字段（失败则只存纯文本，
   绝不能影响文本捕获）；Linux 尽力而为。`copy` 时有 html 则同时写回 HTML 与纯文本。
8. 锁屏检测：Windows 用 WTS session notification（x/sys/windows，纯 Go）；
   Linux 监听 D-Bus `org.freedesktop.ScreenSaver` / `org.gnome.ScreenSaver` 的
   `ActiveChanged` 信号；失败则该功能静默不可用。
9. 自动备份：`auto_backup` 开启时，每 `backup_interval_days` 天在 db 同目录生成
   `clipvault-backup-YYYYMMDD-HHmmss.cvb
...[truncated 1014 chars]