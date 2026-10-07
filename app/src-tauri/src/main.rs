#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::collections::HashMap;
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::Stdio;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde_json::{json, Value};
use tauri::menu::{MenuBuilder, MenuItemBuilder, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconEvent, TrayIconBuilder};
use tauri::{AppHandle, Emitter, Listener, Manager, State, WindowEvent};
use tauri_plugin_autostart::ManagerExt as _;
use tauri_plugin_dialog::DialogExt;
use tauri_plugin_global_shortcut::{GlobalShortcutExt, Shortcut, ShortcutState};

// ---------------------------------------------------------------------------
// Go sidecar bridge: newline-delimited JSON-RPC 2.0 over stdin/stdout
// ---------------------------------------------------------------------------

struct CoreBridge {
    stdin: Mutex<std::process::ChildStdin>,
    next_id: AtomicU64,
    pending: Mutex<HashMap<u64, tokio::sync::oneshot::Sender<Result<Value, String>>>>,
}

impl CoreBridge {
    async fn call(&self, method: &str, params: Value, timeout: Duration) -> Result<Value, String> {
        let id = self.next_id.fetch_add(1, Ordering::SeqCst) + 1;
        let (tx, rx) = tokio::sync::oneshot::channel();
        {
            let mut p = self.pending.lock().map_err(|_| "内部锁错误".to_string())?;
            p.insert(id, tx);
        }
        let line = serde_json::to_string(&json!({
            "jsonrpc": "2.0", "id": id, "method": method, "params": params,
        }))
        .map_err(|e| e.to_string())?;
        {
            let mut stdin = self.stdin.lock().map_err(|_| "内部锁错误".to_string())?;
            let w = writeln!(stdin, "{}", line)
                .and_then(|_| stdin.flush())
                .map_err(|e| format!("核心进程写入失败: {}", e));
            if w.is_err() {
                self.pending.lock().ok().map(|mut p| p.remove(&id));
                return w.map(|_| Value::Null);
            }
        }
        match tokio::time::timeout(timeout, rx).await {
            Ok(Ok(r)) => r,
            Ok(Err(_)) => Err("核心进程无响应".to_string()),
            Err(_) => {
                self.pending.lock().ok().map(|mut p| p.remove(&id));
                Err("核心进程响应超时".to_string())
            }
        }
    }
}

fn triple_suffix() -> &'static str {
    #[cfg(all(target_os = "windows", target_arch = "x86_64"))]
    return "x86_64-pc-windows-msvc";
    #[cfg(all(target_os = "windows", target_arch = "aarch64"))]
    return "aarch64-pc-windows-msvc";
    #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
    return "x86_64-unknown-linux-gnu";
    #[cfg(all(target_os = "linux", target_arch = "aarch64"))]
    return "aarch64-unknown-linux-gnu";
    #[cfg(not(any(
        all(target_os = "windows", target_arch = "x86_64"),
        all(target_os = "windows", target_arch = "aarch64"),
        all(target_os = "linux", target_arch = "x86_64"),
        all(target_os = "linux", target_arch = "aarch64"),
    )))]
    return "unknown";
}

/// sidecar 查找顺序：
/// 1. 打包后的 resources/binaries/clipvault-core-<triple>[.exe]
/// 2. 便携版/开发模式：exe 同目录的 clipvault-core[.exe]
fn sidecar_candidates(app: &AppHandle) -> Vec<PathBuf> {
    let mut v = Vec::new();
    let bundled =
        format!("clipvault-core-{}{}", triple_suffix(), std::env::consts::EXE_SUFFIX);
    if let Ok(rd) = app.path().resource_dir() {
        v.push(rd.join("binaries").join(&bundled));
    }
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            v.push(dir.join(format!("clipvault-core{}", std::env::consts::EXE_SUFFIX)));
        }
    }
    v
}

fn spawn_core(app: &AppHandle, bridge_holder: &Arc<Mutex<Option<Arc<CoreBridge>>>>) -> Result<(), String> {
    let mut child_opt = None;
    for cand in sidecar_candidates(app) {
        if !cand.exists() {
            continue;
        }
        match std::process::Command::new(&cand)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::inherit())
            .spawn()
        {
            Ok(c) => {
                child_opt = Some(c);
                break;
            }
            Err(_) => continue,
        }
    }
    let mut child = child_opt.ok_or_else(|| "找不到 clipvault-core 核心程序".to_string())?;
    let stdin = child.stdin.take().ok_or("无法接管核心进程输入")?;
    let stdout = child.stdout.take().ok_or("无法接管核心进程输出")?;

    let bridge = Arc::new(CoreBridge {
        stdin: Mutex::new(stdin),
        next_id: AtomicU64::new(0),
        pending: Mutex::new(HashMap::new()),
    });
    *bridge_holder.lock().map_err(|_| "内部锁错误".to_string())? = Some(bridge.clone());

    // 读取线程：响应按 id 回填，事件转发给前端
    let app2 = app.clone();
    std::thread::spawn(move || {
        let reader = BufReader::new(stdout);
        for line in reader.lines().map_while(Result::ok) {
            let v: Value = match serde_json::from_str(&line) {
                Ok(v) => v,
                Err(_) => continue,
            };
            if let Some(id) = v.get("id").and_then(|i| i.as_u64()) {
                let tx = bridge.pending.lock().ok().and_then(|mut p| p.remove(&id));
                if let Some(tx) = tx {
                    if let Some(err) = v.get("error") {
                        let msg = err
                            .get("message")
                            .and_then(|m| m.as_str())
                            .unwrap_or("核心返回错误");
                        let _ = tx.send(Err(msg.to_string()));
                    } else {
                        let _ = tx.send(Ok(v.get("result").cloned().unwrap_or(Value::Null)));
                    }
                }
            } else if let Some(ev) = v.get("event").and_then(|e| e.as_str()) {
                if ev == "state_changed" {
                    update_tray_recording(&app2, v.get("data").and_then(|d| d.get("recording")).and_then(|r| r.as_bool()).unwrap_or(true));
                }
                let _ = app2.emit("core-event", &v);
            }
        }
    });
    Ok(())
}

// ---------------------------------------------------------------------------
// 托盘
// ---------------------------------------------------------------------------

const ICON_RECORDING: &[u8] = include_bytes!("../icons/tray-recording.png");
const ICON_PAUSED: &[u8] = include_bytes!("../icons/tray-paused.png");

fn tray_icon(recording: bool) -> Option<tauri::image::Image<'static>> {
    let bytes = tray_icon_bytes(recording);
    let rgba = image::load_from_memory(&bytes).ok()?.to_rgba8();
    let (w, h) = rgba.dimensions();
    Some(tauri::image::Image::new_owned(rgba.into_raw(), w, h))
}

fn tray_icon_bytes(recording: bool) -> Vec<u8> {
    if recording { ICON_RECORDING.to_vec() } else { ICON_PAUSED.to_vec() }
}

fn rebuild_tray(app: &AppHandle, recording: bool) {
    let toggle_label = if recording { "⏸  暂停记录" } else { "▶  恢复记录" };
    let menu = MenuBuilder::new(app)
        .item(&MenuItemBuilder::with_id("open_main", "📋  打开历史记录").build(app).unwrap())
        .item(&MenuItemBuilder::with_id("open_popup", "⚡  快速粘贴面板").build(app).unwrap())
        .item(&PredefinedMenuItem::separator(app).unwrap())
        .item(&MenuItemBuilder::with_id("toggle_rec", toggle_label).build(app).unwrap())
        .item(&MenuItemBuilder::with_id("open_settings", "⚙  设置").build(app).unwrap())
        .item(&MenuItemBuilder::with_id("backup_now", "💾  立即备份").build(app).unwrap())
        .item(&PredefinedMenuItem::separator(app).unwrap())
        .item(&MenuItemBuilder::with_id("quit", "✖  退出").build(app).unwrap())
        .build()
        .unwrap();
    if let Some(tray) = app.tray_by_id("main") {
        let _ = tray.set_menu(Some(menu));
        if let Some(img) = tray_icon(recording) {
            let _ = tray.set_icon(Some(img));
        }
        let _ = tray.set_tooltip(Some(if recording {
            "ClipVault-Local（记录中）"
        } else {
            "ClipVault-Local（已暂停）"
        }));
    }
}

fn update_tray_recording(app: &AppHandle, recording: bool) {
    if let Ok(mut g) = app.state::<AppState>().recording.lock() {
        *g = recording;
    }
    rebuild_tray(app, recording);
}

#[derive(Clone)]
struct AppState {
    bridge: Arc<Mutex<Option<Arc<CoreBridge>>>>,
    recording: Arc<Mutex<bool>>,
}

// ---------------------------------------------------------------------------
// 命令（前端调用）
// ---------------------------------------------------------------------------

fn timeout_for(method: &str) -> Duration {
    match method {
        "import_backup" | "export_backup" | "export_json" | "auto_backup_now" => Duration::from_secs(120),
        _ => Duration::from_secs(30),
    }
}

#[tauri::command]
async fn core_request(
    app: AppHandle,
    state: State<'_, AppState>,
    method: String,
    params: Option<Value>,
) -> Result<String, String> {
    let bridge = state
        .bridge
        .lock()
        .map_err(|_| "内部锁错误".to_string())?
        .clone()
        .ok_or_else(|| "核心进程尚未启动".to_string())?;
    let v = bridge
        .call(&method, params.unwrap_or(Value::Null), timeout_for(&method))
        .await?;
    serde_json::to_string(&v).map_err(|e| e.to_string())
}

#[tauri::command]
fn set_hotkey(app: AppHandle, hotkey: String) -> Result<(), String> {
    let gs = app.global_shortcut();
    let _ = gs.unregister_all();
    let hk = hotkey.trim().to_string();
    if hk.is_empty() {
        return Ok(());
    }
    let sc: Shortcut = hk.parse().map_err(|_| format!("热键“{}”格式不对，已跳过注册", hk))?;
    let app2 = app.clone();
    gs.on_shortcut(sc, move |_a, _s, event| {
        if event.state == ShortcutState::Pressed {
            toggle_popup(&app2);
        }
    })
    .map_err(|e| format!("注册热键失败: {}", e))?;
    Ok(())
}

#[tauri::command]
fn set_autostart(app: AppHandle, enabled: bool) -> Result<(), String> {
    let am = app.autolaunch();
    if enabled {
        am.enable().map_err(|e| format!("开启开机自启失败: {}", e))?;
    } else {
        am.disable().map_err(|e| format!("关闭开机自启失败: {}", e))?;
    }
    Ok(())
}

#[tauri::command]
fn autostart_enabled(app: AppHandle) -> Result<bool, String> {
    app.autolaunch()
        .is_enabled()
        .map_err(|e| format!("读取开机自启状态失败: {}", e))
}

fn show_window(app: &AppHandle, label: &str) {
    if let Some(w) = app.get_webview_window(label) {
        let _ = w.show();
        let _ = w.set_focus();
        if label == "popup" {
            let _ = w.center();
        }
    }
}

fn toggle_popup(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("popup") {
        match w.is_visible() {
            Ok(true) => {
                let _ = w.hide();
            }
            _ => show_window(app, "popup"),
        }
    }
}

#[tauri::command]
fn show_main(app: AppHandle) {
    show_window(&app, "main");
}

#[tauri::command]
fn show_popup_cmd(app: AppHandle) {
    show_window(&app, "popup");
}

#[tauri::command]
fn show_settings(app: AppHandle) {
    show_window(&app, "settings");
}

#[tauri::command]
fn hide_popup(app: AppHandle) {
    if let Some(w) = app.get_webview_window("popup") {
        let _ = w.hide();
    }
}

#[tauri::command]
fn pick_save_path(app: AppHandle, default_name: String) -> Result<Option<String>, String> {
    let p = app
        .dialog()
        .file()
        .set_file_name(&default_name)
        .blocking_save_file();
    Ok(p.map(|f| f.to_string()))
}

#[tauri::command]
fn pick_dir(app: AppHandle) -> Result<Option<String>, String> {
    let p = app.dialog().file().blocking_pick_folder();
    Ok(p.map(|f| f.to_string()))
}

#[tauri::command]
fn pick_open_file(app: AppHandle) -> Result<Option<String>, String> {
    let p = app
        .dialog()
        .file()
        .add_filter("备份包", &["cvbak"])
        .blocking_pick_file();
    Ok(p.map(|f| f.to_string()))
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------

fn main() {
    let bridge_holder: Arc<Mutex<Option<Arc<CoreBridge>>>> = Arc::new(Mutex::new(None));
    let recording = Arc::new(Mutex::new(true));

    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_global_shortcut::Builder::new().build())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            Some(vec!["--minimized"]),
        ))
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            show_window(app, "main");
        }))
        .manage(AppState {
            bridge: bridge_holder.clone(),
            recording: recording.clone(),
        })
        .setup(move |app| {
            // 1. 启动 Go 核心
            if let Err(e) = spawn_core(&app.handle(), &bridge_holder) {
                eprintln!("clipvault-core 启动失败: {}", e);
            }

            // 2. 托盘
            let app_h = app.handle().clone();
            let tray = TrayIconBuilder::with_id("main")
                .on_menu_event(move |app, event| match event.id.as_ref() {
                    "open_main" => show_window(app, "main"),
                    "open_popup" => show_window(app, "popup"),
                    "open_settings" => show_window(app, "settings"),
                    "toggle_rec" => {
                        let cur = app
                            .state::<AppState>()
                            .recording
                            .lock()
                            .map(|g| *g)
                            .unwrap_or(true);
                        let app2 = app.clone();
                        tauri::async_runtime::spawn(async move {
                            let bridge = app2
                                .state::<AppState>()
                                .bridge
                                .lock()
                                .ok()
                                .and_then(|g| g.clone());
                            if let Some(b) = bridge {
                                if let Ok(v) = b
                                    .call("set_recording", json!({"enabled": !cur}), Duration::from_secs(10))
                                    .await
                                {
                                    let rec = v
                                        .get("recording")
                                        .and_then(|r| r.as_bool())
                                        .unwrap_or(!cur);
                                    update_tray_recording(&app2, rec);
                                }
                            }
                        });
                    }
                    "backup_now" => {
                        let app2 = app.clone();
                        tauri::async_runtime::spawn(async move {
                            let bridge = app2
                                .state::<AppState>()
                                .bridge
                                .lock()
                                .ok()
                                .and_then(|g| g.clone());
                            if let Some(b) = bridge {
                                match b
                                    .call("auto_backup_now", Value::Null, Duration::from_secs(120))
                                    .await
                                {
                                    Ok(v) => {
                                        let path = v
                                            .get("path")
                                            .and_then(|p| p.as_str())
                                            .unwrap_or("备份目录");
                                        app2.dialog()
                                            .message(format!("备份已保存到：\n{}", path))
                                            .title("备份完成")
                                            .blocking_show();
                                    }
                                    Err(e) => {
                                        app2.dialog()
                                            .message(format!("备份失败：{}", e))
                                            .title("备份失败")
                                            .blocking_show();
                                    }
                                }
                            }
                        });
                    }
                    "quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, event| {
                    if let TrayIconEvent::Click {
                        button: MouseButton::Left,
                        button_state: MouseButtonState::Up,
                        ..
                    } = event
                    {
                        let app = tray.app_handle();
                        show_window(app, "main");
                    }
                })
                .build(&app_h)
                .expect("托盘创建失败");
            let _ = tray.set_tooltip(Some("ClipVault-Local（记录中）"));
            rebuild_tray(&app_h, true);

            // 3. popup 失焦自动隐藏
            if let Some(popup) = app.get_webview_window("popup") {
                let app_h2 = app.handle().clone();
                popup.on_window_event(move |event| {
                    if let WindowEvent::Focused(false) = event {
                        if let Some(w) = app_h2.get_webview_window("popup") {
                            let _ = w.hide();
                        }
                    }
                });
            }

            // 5. popup 双击条目 → 打开主窗口（主窗口前端自己选中该条目）
            let app_h4 = app.handle().clone();
            app.handle().listen("clipvault-open-entry", move |_event| {
                show_window(&app_h4, "main");
            });

            // 6. 读取设置并注册全局热键（核心就绪后稍等片刻）
            let app_h3 = app.handle().clone();
            let bh = bridge_holder.clone();
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(Duration::from_millis(800)).await;
                let bridge = bh.lock().ok().and_then(|g| g.clone());
                if let Some(b) = bridge {
                    if let Ok(v) = b.call("get_settings", Value::Null, Duration::from_secs(10)).await {
                        if let Some(hk) = v.get("hotkey").and_then(|h| h.as_str()) {
                            let _ = set_hotkey(app_h3.clone(), hk.to_string());
                        }
                        if let Some(rec) = v.get("recording").and_then(|r| r.as_bool()) {
                            update_tray_recording(&app_h3, rec);
                        }
                    }
                }
            });

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            core_request,
            set_hotkey,
            set_autostart,
            autostart_enabled,
            show_main,
            show_popup_cmd,
            show_settings,
            hide_popup,
            pick_save_path,
            pick_dir,
            pick_open_file,
        ])
        .run(tauri::generate_context!())
        .expect("ClipVault-Local 启动失败");
}
