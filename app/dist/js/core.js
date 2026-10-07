/* core.js — ClipVault-Local 前端通信层
 * 真实模式：通过 window.__TAURI__ 调用 Rust 命令 core_request({method, params})，
 *           返回 JSON 字符串；监听 "core-event" 事件。
 * mock 模式（URL 带 ?mock=1，或直接在浏览器打开）：内置 12 条中文示例数据，
 *           所有操作只改本地内存状态，用于无后端截图验证。
 */
(function () {
  'use strict';

  var qs = new URLSearchParams(location.search);
  var MOCK = qs.get('mock') === '1' || typeof window.__TAURI__ === 'undefined';

  // ---------- 事件总线 ----------
  var listeners = {};   // name -> [fn]
  function onEvent(name, fn) {
    (listeners[name] = listeners[name] || []).push(fn);
  }
  function emitLocal(name, data) {
    (listeners[name] || []).forEach(function (fn) {
      try { fn(data); } catch (e) { console.error(e); }
    });
  }

  // ---------- mock 后端 ----------
  var mock = null;
  function nowMs() { return Date.now(); }
  function minutesAgo(m) { return Date.now() - m * 60000; }

  // 用 canvas 生成示例缩略图（零外部资源）
  function makeMockImage(w, h, label, hue) {
    var c = document.createElement('canvas');
    c.width = w; c.height = h;
    var g = c.getContext('2d');
    var grad = g.createLinearGradient(0, 0, w, h);
    grad.addColorStop(0, 'hsl(' + hue + ',45%,26%)');
    grad.addColorStop(1, 'hsl(' + (hue + 40) + ',50%,16%)');
    g.fillStyle = grad; g.fillRect(0, 0, w, h);
    // 假装是窗口截图：标题栏 + 几行内容块
    g.fillStyle = 'rgba(255,255,255,0.14)';
    g.fillRect(0, 0, w, Math.round(h * 0.09));
    g.fillStyle = 'hsl(' + hue + ',70%,60%)';
    [0.16, 0.26, 0.36, 0.46, 0.56].forEach(function (y, i) {
      g.fillRect(Math.round(w * 0.06), Math.round(h * y), Math.round(w * (0.88 - i * 0.09)), Math.round(h * 0.05));
    });
    g.fillStyle = 'rgba(255,255,255,0.85)';
    g.font = 'bold ' + Math.round(h * 0.06) + 'px sans-serif';
    g.fillText(label, Math.round(w * 0.06), Math.round(h * 0.065));
    return c.toDataURL('image/png');
  }

  function buildMock() {
    var entries = [
      { id: 12, kind: 'text', text: '您的验证码是 482913，请在10分钟内完成验证，切勿泄露给他人。', tags: ['工作'], favorite: false, sensitive: true, created_at: minutesAgo(2) },
      { id: 11, kind: 'image', text: '', tags: ['工作'], favorite: false, sensitive: false, created_at: minutesAgo(9), imgW: 1280, imgH: 800, imgLabel: '会议纪要截图', imgHue: 210 },
      { id: 10, kind: 'text', text: '13812345678，张师傅，明天上午十点上门修空调，记得提前把阳台收拾一下。', tags: ['工作'], favorite: false, sensitive: false, created_at: minutesAgo(25) },
      { id: 9, kind: 'text', text: '订单号：JD20261007123456\n商品：儿童保温杯 500ml 蓝色\n状态：已发货，预计后天送达', tags: ['订单'], favorite: true, sensitive: false, created_at: minutesAgo(47) },
      { id: 8, kind: 'text', text: '秋天第一杯奶茶文案：\n天凉了，记得添衣。\n你负责热爱生活，我负责给你点奶茶。', tags: [], favorite: false, sensitive: false, created_at: minutesAgo(80) },
      { id: 7, kind: 'image', text: '', tags: [], favorite: false, sensitive: false, created_at: minutesAgo(130), imgW: 900, imgH: 1200, imgLabel: '菜谱截图', imgHue: 30 },
      { id: 6, kind: 'text', text: '上海市浦东新区张江高科技园区科苑路 88 号 3 号楼 205 室，前台签收，电话 13812345678', tags: ['地址'], favorite: true, sensitive: false, created_at: minutesAgo(190) },
      { id: 5, kind: 'text', text: '双十一预售攻略：先领券再下单，满300减50，叠加店铺券更划算，记得定好闹钟抢前100名半价。', tags: ['购物'], favorite: false, sensitive: false, created_at: minutesAgo(320) },
      { id: 4, kind: 'text', text: 'ziyi.wang@example.com', tags: ['工作'], favorite: false, sensitive: false, created_at: minutesAgo(500) },
      { id: 3, kind: 'image', text: '', tags: ['学习'], favorite: false, sensitive: false, created_at: minutesAgo(700), imgW: 1440, imgH: 900, imgLabel: '课程表截图', imgHue: 150 },
      { id: 2, kind: 'text', text: '番茄炒蛋做法：\n1. 鸡蛋打散加少许盐，热油炒至凝固盛出\n2. 番茄切块下锅，加一勺糖提鲜\n3. 倒回鸡蛋翻炒，加葱花出锅', tags: ['学习'], favorite: false, sensitive: false, created_at: minutesAgo(1500) },
      { id: 1, kind: 'text', text: '本周例会纪要要点：\n- 周三前提交季度总结\n- 新品上线时间提前到下周五\n- 客服话术统一更新到 v3 版', tags: ['工作'], favorite: true, sensitive: false, created_at: minutesAgo(2900) },
    ];
    entries.forEach(function (e) {
      e.has_image = e.kind === 'image';
      e.text_len = e.text.length;
      e.preview = e.kind === 'image'
        ? ('图片 · ' + e.imgW + '×' + e.imgH)
        : (e.text.length > 60 ? e.text.slice(0, 60) + '…' : e.text);
      if (e.kind === 'image') {
        e._thumb = makeMockImage(320, Math.round(320 * e.imgH / e.imgW), e.imgLabel, e.imgHue);
        e._full = makeMockImage(e.imgW, e.imgH, e.imgLabel + '（原图）', e.imgHue);
      }
    });
    return {
      entries: entries,
      nextId: 100,
      settings: {
        recording: true, max_entries: 200, record_images: true, image_space_mb: 200,
        record_text: true, hotkey: 'Shift+Super+V', theme: 'auto', autostart: false,
        skip_sensitive: 'ask', db_dir: '', auto_backup: true, backup_interval_days: 7,
        lock_action: 'none', db_password_set: false, onboarded: false,
      },
      state: { version: '0.1.0-mock', db_path: 'C:\\Users\\演示\\Documents\\ClipVault-Local\\clipvault.db' },
      pendingSensitive: [],
    };
  }

  function mockSummary(e) {
    return { id: e.id, kind: e.kind, preview: e.preview, created_at: e.created_at,
      favorite: e.favorite, tags: e.tags.slice(), has_image: e.has_image,
      text_len: e.text_len, sensitive: !!e.sensitive };
  }
  function mockDetail(e) {
    var d = mockSummary(e);
    d.text = e.text;
    if (e.kind === 'image') {
      d.image_thumb = e._thumb; d.image_full = e._full;
      d.image_w = e.imgW; d.image_h = e.imgH;
    }
    return d;
  }
  function mockState() {
    var imgs = mock.entries.filter(function (e) { return e.kind === 'image'; });
    return {
      recording: mock.settings.recording,
      entry_count: mock.entries.length,
      text_count: mock.entries.filter(function (e) { return e.kind === 'text'; }).length,
      image_count: imgs.length,
      image_bytes: imgs.length * 240000,
      db_path: mock.state.db_path,
      version: mock.state.version,
    };
  }

  function mockRequest(method, params) {
    params = params || {};
    switch (method) {
      case 'ping': return { version: mock.state.version };
      case 'get_state': return mockState();
      case 'set_recording':
        mock.settings.recording = !!params.enabled;
        emitLocal('core-event', { event: 'state_changed', data: { recording: mock.settings.recording } });
        return { recording: mock.settings.recording };
      case 'list': {
        var q = (params.query || '').toLowerCase();
        var list = mock.entries.filter(function (e) {
          if (params.tag && e.tags.indexOf(params.tag) < 0) return false;
          if (params.kind && e.kind !== params.kind) return false;
          if (params.favorites_only && !e.favorite) return false;
          if (q && (e.text.toLowerCase().indexOf(q) < 0) &&
                   e.tags.join(' ').toLowerCase().indexOf(q) < 0) return false;
          return true;
        });
        list.sort(function (a, b) { return b.created_at - a.created_at; });
        var total = list.length;
        var off = params.offset || 0, lim = params.limit || 50;
        return { entries: list.slice(off, off + lim).map(mockSummary), total: total };
      }
      case 'get': {
        var g = mock.entries.filter(function (e) { return e.id === params.id; })[0];
        if (!g) throw new Error('条目不存在');
        return mockDetail(g);
      }
      case 'copy': toast('已复制到剪贴板（演示）'); return { ok: true };
      case 'set_favorite': {
        var f = mock.entries.filter(function (e) { return e.id === params.id; })[0];
        if (f) f.favorite = !!params.favorite;
        return { ok: true };
      }
      case 'add_tag': {
        var a = mock.entries.filter(function (e) { return e.id === params.id; })[0];
        if (a && params.tag && a.tags.indexOf(params.tag) < 0) a.tags.push(params.tag);
        return { tags: a ? a.tags.slice() : [] };
      }
      case 'remove_tag': {
        var r = mock.entries.filter(function (e) { return e.id === params.id; })[0];
        if (r) r.tags = r.tags.filter(function (t) { return t !== params.tag; });
        return { tags: r ? r.tags.slice() : [] };
      }
      case 'list_tags': {
        var cnt = {};
        mock.entries.forEach(function (e) { e.tags.forEach(function (t) { cnt[t] = (cnt[t] || 0) + 1; }); });
        return { tags: Object.keys(cnt).sort(function (a, b) { return cnt[b] - cnt[a]; }) };
      }
      case 'delete':
        mock.entries = mock.entries.filter(function (e) { return e.id !== params.id; });
        emitLocal('core-event', { event: 'entry_deleted', data: { id: params.id } });
        return { ok: true };
      case 'clear_all': {
        var n = mock.entries.length; mock.entries = [];
        emitLocal('core-event', { event: 'entries_cleared', data: { deleted: n } });
        return { deleted: n };
      }
      case 'clear_recent': {
        var cut = Date.now() - (params.minutes || 60) * 60000;
        var before = mock.entries.length;
        mock.entries = mock.entries.filter(function (e) { return e.created_at < cut; });
        return { deleted: before - mock.entries.length };
      }
      case 'get_settings': return JSON.parse(JSON.stringify(mock.settings));
      case 'set_settings':
        Object.keys(params.patch || {}).forEach(function (k) {
          if (k in mock.settings) mock.settings[k] = params.patch[k];
        });
        emitLocal('core-event', { event: 'settings_changed', data: {} });
        return JSON.parse(JSON.stringify(mock.settings));
      case 'export_json': toast('已导出 JSON（演示）'); return { dir: params.dir || '演示目录', files: 3 };
      case 'export_backup': toast('备份包已生成（演示）'); return { path: 'clipvault-演示.cvb', bytes: 1024 };
      case 'import_backup': toast('导入完成（演示）'); return { imported: 0, skipped: 0 };
      case 'auto_backup_now': return { path: 'clipvault-backup-演示.cvb' };
      case 'save_image': {
        var s = mock.entries.filter(function (e) { return e.id === params.id; })[0];
        if (s && s.kind === 'image') {
          var aEl = document.createElement('a');
          aEl.href = s._full; aEl.download = 'clipvault-' + s.id + '.png';
          document.body.appendChild(aEl); aEl.click(); aEl.remove();
        }
        return { path: params.path || aEl.download };
      }
      case 'resolve_sensitive': {
        var p = mock.pendingSensitive.filter(function (x) { return x.token === params.token; })[0];
        if (p && params.record) {
          var e2 = { id: mock.nextId++, kind: 'text', text: p.preview, tags: [],
            favorite: false, sensitive: true, created_at: Date.now(), has_image: false,
            text_len: p.preview.length,
            preview: p.preview.length > 60 ? p.preview.slice(0, 60) + '…' : p.preview };
          mock.entries.unshift(e2);
          emitLocal('core-event', { event: 'entry_added', data: { id: e2.id, kind: 'text' } });
        }
        return { ok: true };
      }
      case 'set_db_password':
        mock.settings.db_password_set = !!params.password;
        return { ok: true };
      default: throw new Error('未知方法: ' + method);
    }
  }

  // mock 下演示敏感确认：由页面上的演示按钮触发
  function mockFireSensitive() {
    if (!mock) return;
    var token = 'mock-token-' + Date.now();
    mock.pendingSensitive.push({ token: token, preview: '您的登录密码已重置为 Abc12345，请及时修改。' });
    emitLocal('core-event', { event: 'sensitive_prompt',
      data: { token: token, preview: '您的登录密码已重置为 Abc12345，请及时修改。', kind: 'text' } });
  }

  // ---------- 真实请求 ----------
  function realRequest(method, params) {
    return window.__TAURI__.core.invoke('core_request', { method: method, params: params || {} })
      .then(function (json) {
        try { return JSON.parse(json); }
        catch (e) { throw new Error('后端返回无法解析: ' + json); }
      });
  }

  function request(method, params) {
    if (MOCK) {
      return new Promise(function (resolve, reject) {
        setTimeout(function () {
          try { resolve(mockRequest(method, params)); }
          catch (e) { reject(e); }
        }, 60);
      });
    }
    return realRequest(method, params);
  }

  // 跨窗口自定义事件（popup → main）：clipvault-open-entry
  function emitAppEvent(name, data) {
    if (MOCK) { emitLocal(name, data); return Promise.resolve(); }
    return window.__TAURI__.event.emit(name, data || {});
  }
  function onAppEvent(name, fn) {
    if (MOCK) { onEvent(name, fn); return; }
    window.__TAURI__.event.listen(name, function (e) { fn(e.payload); });
  }

  function closeWindow() {
    if (!MOCK && window.__TAURI__ && window.__TAURI__.window) {
      try { window.__TAURI__.window.getCurrentWindow().close(); return; } catch (e) {}
    }
    window.close();
  }

  // ---------- 文件对话框（走 Rust 命令；无 Tauri 时降级为输入框） ----------
  function hasTauri() {
    try { return !MOCK && window.__TAURI__ && window.__TAURI__.core; }
    catch (e) { return false; }
  }
  function pickSave(defaultName) {
    if (hasTauri()) return window.__TAURI__.core.invoke('pick_save_path', { defaultName: defaultName || '' }).catch(function () { return null; });
    return Promise.resolve(prompt('请输入保存路径（含文件名）', defaultName || ''));
  }
  function pickOpenDir() {
    if (hasTauri()) return window.__TAURI__.core.invoke('pick_dir').catch(function () { return null; });
    return Promise.resolve(prompt('请输入文件夹路径', ''));
  }
  function pickOpenFile() {
    if (hasTauri()) return window.__TAURI__.core.invoke('pick_open_file').catch(function () { return null; });
    return Promise.resolve(prompt('请输入备份包（.cvbak）路径', ''));
  }

  // ---------- UI 小工具 ----------
  function toast(msg) {
    var wrap = document.querySelector('.toast-wrap');
    if (!wrap) { wrap = document.createElement('div'); wrap.className = 'toast-wrap'; document.body.appendChild(wrap); }
    var el = document.createElement('div');
    el.className = 'toast'; el.textContent = msg;
    wrap.appendChild(el);
    setTimeout(function () { el.remove(); }, 2600);
  }

  // 通用确认/选择对话框，返回 Promise<按钮 id>
  function ask(title, body, buttons, opts) {
    opts = opts || {};
    return new Promise(function (resolve) {
      var mask = document.createElement('div');
      mask.className = 'modal-mask';
      var html = '<div class="modal"><h3></h3>';
      if (opts.snippet) html += '<div class="preview-snippet selectable"></div>';
      html += '<p></p><div class="btn-row"></div></div>';
      mask.innerHTML = html;
      mask.querySelector('h3').textContent = title;
      mask.querySelector('p').textContent = body;
      if (opts.snippet) mask.querySelector('.preview-snippet').textContent = opts.snippet;
      var row = mask.querySelector('.btn-row');
      buttons.forEach(function (b) {
        var btn = document.createElement('button');
        btn.className = 'btn' + (b.primary ? ' btn-primary' : '') + (b.danger ? ' btn-danger' : '');
        btn.textContent = b.text;
        btn.onclick = function () { mask.remove(); resolve(b.id); };
        row.appendChild(btn);
      });
      mask.addEventListener('click', function (e) {
        if (e.target === mask && opts.dismissable !== false) { mask.remove(); resolve(opts.cancelId || 'cancel'); }
      });
      document.body.appendChild(mask);
      var first = row.querySelector('.btn-primary') || row.querySelector('.btn');
      if (first) first.focus();
    });
  }

  function fmtTime(ts) {
    var d = new Date(ts), now = new Date();
    var diff = now - d;
    if (diff < 60000) return '刚刚';
    if (diff < 3600000) return Math.floor(diff / 60000) + ' 分钟前';
    if (d.toDateString() === now.toDateString()) return Math.floor(diff / 3600000) + ' 小时前';
    var y = new Date(now); y.setDate(now.getDate() - 1);
    if (d.toDateString() === y.toDateString()) return '昨天 ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
    return (d.getMonth() + 1) + '-' + d.getDate() + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  function pad(n) { return (n < 10 ? '0' : '') + n; }

  // 快捷键：存储格式 "Shift+Super+V" ⇄ 显示 "Win + Shift + V"
  var HK_ORDER = { Super: 0, Win: 0, Control: 1, Ctrl: 1, Alt: 2, Shift: 3 };
  function hotkeyToDisplay(hk) {
    if (!hk) return '已关闭';
    return hk.split('+').map(function (p) {
      return p === 'Super' ? 'Win' : (p === 'Control' ? 'Ctrl' : p);
    }).sort(function (a, b) {
      return (HK_ORDER[a] == null ? 9 : HK_ORDER[a]) - (HK_ORDER[b] == null ? 9 : HK_ORDER[b]);
    }).join(' + ');
  }

  // 主题：auto 跟随系统
  function applyTheme(theme) {
    var dark = true;
    if (theme === 'light') dark = false;
    else if (theme === 'dark') dark = true;
    else if (window.matchMedia) dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
  }
  if (window.matchMedia) {
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function () {
      if (Core._theme === 'auto' || !Core._theme) applyTheme(Core._theme || 'auto');
    });
  }

  var Core = {
    mock: MOCK,
    request: request,
    onEvent: function (fn) { onEvent('core-event', fn); },
    onAppEvent: onAppEvent,
    emitAppEvent: emitAppEvent,
    closeWindow: closeWindow,
    pickSave: pickSave, pickOpenDir: pickOpenDir, pickOpenFile: pickOpenFile,
    toast: toast, ask: ask, fmtTime: fmtTime,
    hotkeyToDisplay: hotkeyToDisplay,
    applyTheme: applyTheme,
    mockFireSensitive: mockFireSensitive,
    _theme: 'auto',
  };
  window.Core = Core;

  // ---------- 启动 ----------
  if (MOCK) mock = buildMock();
  else if (window.__TAURI__ && window.__TAURI__.event) {
    window.__TAURI__.event.listen('core-event', function (e) { emitLocal('core-event', e.payload); })
      .catch(function (err) { console.error('事件监听失败', err); });
  }

  // 全局：敏感确认对话框
  onEvent('core-event', function (payload) {
    if (!payload || payload.event !== 'sensitive_prompt') return;
    var d = payload.data || {};
    ask('检测到可能是密码 / 验证码',
      '这条复制内容里出现了"密码""验证码"之类的词。为保护隐私，你要把它存进历史吗？',
      [{ id: 'skip', text: '跳过，不记录' }, { id: 'record', text: '记录', primary: true }],
      { snippet: d.preview || '', dismissable: true, cancelId: 'skip' }
    ).then(function (choice) {
      Core.request('resolve_sensitive', { token: d.token, record: choice === 'record' })
        .catch(function (e) { toast('处理失败：' + e.message); });
    });
  });

  // 全局：后端错误提示
  onEvent('core-event', function (payload) {
    if (payload && payload.event === 'core_error') toast('出错了：' + (payload.data && payload.data.message));
  });
})();
