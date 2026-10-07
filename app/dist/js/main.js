/* main.js — 主窗口：历史列表 / 搜索 / 标签 / 预览 / 状态栏 / 引导 */
(function () {
  'use strict';

  var state = {
    entries: [], total: 0, selectedId: null, selectedDetail: null,
    query: '', tag: '', settings: null, counts: null, recording: true,
  };

  var $ = function (id) { return document.getElementById(id); };
  var listEl, previewEl, searchInput, chipsEl, statusPill, statusCounts;

  var ALL_TAGS = ['地址', '订单', '工作', '购物'];

  function init() {
    listEl = $('entry-list'); previewEl = $('preview-pane');
    searchInput = $('search'); chipsEl = $('chips');
    statusPill = $('status-pill'); statusCounts = $('status-counts');

    if (Core.mock) {
      var banner = document.createElement('div');
      banner.className = 'mock-banner';
      banner.innerHTML = '演示模式（无后端连接）· 所有操作只改变本地演示数据 ' +
        '<button id="mock-sens">演示"敏感确认"弹窗</button>';
      document.body.insertBefore(banner, document.body.firstChild);
      banner.querySelector('#mock-sens').onclick = function () { Core.mockFireSensitive(); };
    }

    $('btn-toggle-rec').onclick = toggleRecording;
    var debounce = null;
    searchInput.addEventListener('input', function () {
      clearTimeout(debounce);
      debounce = setTimeout(function () { state.query = searchInput.value.trim(); refreshList(); }, 220);
    });

    Core.onEvent(handleCoreEvent);
    Core.onAppEvent('clipvault-open-entry', function (data) {
      if (data && data.id) selectEntry(data.id, true);
    });

    loadAll();
  }

  function loadAll() {
    Core.request('get_settings').then(function (s) {
      state.settings = s;
      Core._theme = s.theme || 'auto';
      Core.applyTheme(s.theme || 'auto');
      state.recording = s.recording !== false;
      renderStatusPill();
      return Core.request('list_tags');
    }).then(function (r) {
      renderChips(r.tags || []);
      refreshList();
      refreshCounts();
      maybeOnboard();
    }).catch(function (e) {
      listEl.innerHTML = '<div class="empty-tip">加载失败：' + escapeHtml(e.message) + '<br>请确认后台程序正在运行。</div>';
    });
  }

  function handleCoreEvent(payload) {
    if (!payload) return;
    switch (payload.event) {
      case 'entry_added':
      case 'entry_deleted':
      case 'entries_cleared':
        refreshList(); refreshCounts(); break;
      case 'state_changed':
        state.recording = !!(payload.data && payload.data.recording);
        renderStatusPill(); break;
      case 'settings_changed':
        Core.request('get_settings').then(function (s) {
          state.settings = s; Core._theme = s.theme || 'auto';
          Core.applyTheme(s.theme || 'auto');
          state.recording = s.recording !== false;
          renderStatusPill();
        });
        break;
    }
  }

  // ---------- 列表 ----------
  function refreshList() {
    Core.request('list', { limit: 100, query: state.query, tag: state.tag })
      .then(function (r) {
        state.entries = r.entries; state.total = r.total;
        renderList();
      }).catch(function (e) { Core.toast('读取历史失败：' + e.message); });
  }

  function renderList() {
    if (!state.entries.length) {
      listEl.innerHTML = '<div class="empty-tip">' +
        (state.query || state.tag ? '没有找到匹配的记录，换个关键词试试。'
          : '还没有复制记录。<br>去复制一段文字或截图，它会自动出现在这里。') + '</div>';
      return;
    }
    listEl.innerHTML = '';
    state.entries.forEach(function (e) {
      var div = document.createElement('div');
      div.className = 'entry' + (e.id === state.selectedId ? ' selected' : '');
      div.dataset.id = e.id;
      var left = e.has_image
        ? '<img class="thumb" src="' + thumbSrc(e) + '" alt="图片">'
        : '<div class="kind-ico">文</div>';
      var tags = (e.tags || []).map(function (t) { return '<span class="tag">' + escapeHtml(t) + '</span>'; }).join('');
      var sens = e.sensitive ? '<span class="sens-flag">疑似敏感</span>' : '';
      div.innerHTML = left +
        '<div class="body"><div class="preview-line">' + escapeHtml(e.preview) + '</div>' +
        '<div class="meta"><span>' + Core.fmtTime(e.created_at) + '</span>' + tags + sens + '</div></div>' +
        (e.favorite ? '<span class="star">★</span>' : '');
      div.addEventListener('click', function () { selectEntry(e.id, false); });
      div.addEventListener('dblclick', function () { copyEntry(e.id); });
      listEl.appendChild(div);
    });
  }

  // 图片条目在 summary 里没有 thumb 数据：mock 下用占位色块，真机由预览时加载
  function thumbSrc(e) {
    if (e._thumbCache) return e._thumbCache;
    // 先用一个内联 SVG 占位，选中预览时再换真实缩略图
    return 'data:image/svg+xml;utf8,' + encodeURIComponent(
      '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64">' +
      '<rect width="64" height="64" fill="#2e3648"/>' +
      '<text x="32" y="38" font-size="22" text-anchor="middle" fill="#a7b0c5">图</text></svg>');
  }

  // ---------- 标签 chips ----------
  function renderChips(usedTags) {
    var tags = [];
    ALL_TAGS.forEach(function (t) { if (tags.indexOf(t) < 0) tags.push(t); });
    (usedTags || []).forEach(function (t) { if (tags.indexOf(t) < 0) tags.push(t); });
    chipsEl.innerHTML = '';
    var all = document.createElement('button');
    all.className = 'chip' + (state.tag === '' ? ' active' : '');
    all.textContent = '全部';
    all.onclick = function () { state.tag = ''; renderChips(usedTags); refreshList(); };
    chipsEl.appendChild(all);
    var fav = document.createElement('button');
    fav.className = 'chip' + (state.tag === '__fav' ? ' active' : '');
    fav.textContent = '★ 收藏';
    fav.onclick = function () { state.tag = '__fav'; renderChips(usedTags); refreshFavList(); };
    chipsEl.appendChild(fav);
    tags.forEach(function (t) {
      var c = document.createElement('button');
      c.className = 'chip' + (state.tag === t ? ' active' : '');
      c.textContent = t;
      c.onclick = function () { state.tag = t; renderChips(usedTags); refreshList(); };
      chipsEl.appendChild(c);
    });
  }

  function refreshFavList() {
    Core.request('list', { limit: 100, query: state.query, favorites_only: true })
      .then(function (r) { state.entries = r.entries; state.total = r.total; renderList(); });
  }

  // ---------- 预览 ----------
  function selectEntry(id, scroll) {
    state.selectedId = id;
    Array.prototype.forEach.call(listEl.querySelectorAll('.entry'), function (el) {
      el.classList.toggle('selected', +el.dataset.id === id);
      if (scroll && +el.dataset.id === id) el.scrollIntoView({ block: 'nearest' });
    });
    previewEl.innerHTML = '<div class="preview-empty">正在加载…</div>';
    Core.request('get', { id: id }).then(function (d) {
      state.selectedDetail = d;
      renderPreview(d);
      // 图片条目：把列表里的占位缩略图换成真缩略图
      if (d.kind === 'image' && d.image_thumb) {
        var row = listEl.querySelector('.entry[data-id="' + id + '"] img.thumb');
        if (row) row.src = d.image_thumb;
      }
    }).catch(function (e) { Core.toast('加载失败：' + e.message); });
  }

  function renderPreview(d) {
    var tags = (d.tags || []).map(function (t) {
      return '<span class="tag">' + escapeHtml(t) + '</span>';
    }).join('');
    var body = '';
    if (d.kind === 'image') {
      body = '<img class="preview-img" id="pv-img" src="' + d.image_thumb +
        '" alt="图片预览"><div class="preview-meta">尺寸 ' + (d.image_w || '?') + ' × ' + (d.image_h || '?') +
        ' · 点击图片查看原图</div>';
    } else {
      body = '<div class="preview-text selectable">' + escapeHtml(d.text || '') + '</div>' +
        '<div class="preview-meta">共 ' + (d.text_len || (d.text || '').length) + ' 字 · ' +
        new Date(d.created_at).toLocaleString('zh-CN') + '</div>';
    }
    previewEl.innerHTML =
      '<div class="preview-title">预览 · ' + (d.kind === 'image' ? '图片' : '文本') + '</div>' +
      body +
      '<div class="preview-tags">' + tags + '</div>' +
      '<div class="preview-actions">' +
        '<button class="btn btn-primary" id="pv-copy">重新复制</button>' +
        '<button class="btn" id="pv-fav">' + (d.favorite ? '★ 取消收藏' : '☆ 收藏') + '</button>' +
        '<button class="btn" id="pv-tag">打标签</button>' +
        (d.kind === 'image' ? '<button class="btn" id="pv-save">另存图片</button>' : '') +
        '<button class="btn btn-danger" id="pv-del">删除</button>' +
      '</div>';

    $('pv-copy').onclick = function () { copyEntry(d.id); };
    $('pv-fav').onclick = function () {
      Core.request('set_favorite', { id: d.id, favorite: !d.favorite }).then(function () {
        Core.toast(d.favorite ? '已取消收藏' : '已收藏，这条不会被自动清理');
        refreshList(); selectEntry(d.id, false);
      }).catch(function (e) { Core.toast('操作失败：' + e.message); });
    };
    $('pv-tag').onclick = function () { openTagEditor(d); };
    $('pv-del').onclick = function () {
      Core.ask('删除这条记录？', '删除后无法恢复，确定要删除吗？',
        [{ id: 'cancel', text: '取消' }, { id: 'del', text: '删除', danger: true }],
        { snippet: d.kind === 'image' ? d.preview : (d.text || '').slice(0, 120) })
        .then(function (c) {
          if (c !== 'del') return;
          Core.request('delete', { id: d.id }).then(function () {
            state.selectedId = null;
            previewEl.innerHTML = '<div class="preview-empty">已删除。点击左侧任意条目查看预览。</div>';
            refreshList(); refreshCounts();
          }).catch(function (e) { Core.toast('删除失败：' + e.message); });
        });
    };
    if (d.kind === 'image') {
      $('pv-img').onclick = function () { openLightbox(d); };
      $('pv-save').onclick = function () { saveImage(d); };
    }
  }

  function copyEntry(id) {
    Core.request('copy', { id: id })
      .then(function () { Core.toast('已复制到剪贴板，去粘贴吧'); })
      .catch(function (e) { Core.toast('复制失败：' + e.message); });
  }

  // ---------- 打标签 ----------
  function openTagEditor(d) {
    Core.request('list_tags').then(function (r) {
      var used = r.tags || [];
      var suggest = [];
      ALL_TAGS.concat(used).forEach(function (t) { if (suggest.indexOf(t) < 0) suggest.push(t); });
      var mask = document.createElement('div');
      mask.className = 'modal-mask';
      mask.innerHTML = '<div class="modal"><h3>给这条记录打标签</h3><p>标签只是个小便签，方便以后按"地址""订单"这样找回来。</p>' +
        '<div class="chips" id="tag-pick" style="padding:0 0 12px"></div>' +
        '<div style="display:flex;gap:8px"><input class="input" id="tag-new" placeholder="或输入新标签，比如：发票" maxlength="12">' +
        '<button class="btn btn-primary" id="tag-add">添加</button></div>' +
        '<div class="btn-row" style="margin-top:14px"><button class="btn" id="tag-done">完成</button></div></div>';
      document.body.appendChild(mask);
      var pick = mask.querySelector('#tag-pick');
      function draw() {
        pick.innerHTML = '';
        suggest.forEach(function (t) {
          var on = d.tags.indexOf(t) >= 0;
          var c = document.createElement('button');
          c.className = 'chip' + (on ? ' active' : '');
          c.textContent = (on ? '✓ ' : '') + t;
          c.onclick = function () {
            var method = on ? 'remove_tag' : 'add_tag';
            Core.request(method, { id: d.id, tag: t }).then(function (res) {
              d.tags = res.tags; draw(); refreshList();
            });
          };
          pick.appendChild(c);
        });
      }
      draw();
      mask.querySelector('#tag-add').onclick = function () {
        var v = mask.querySelector('#tag-new').value.trim();
        if (!v) return;
        Core.request('add_tag', { id: d.id, tag: v }).then(function (res) {
          d.tags = res.tags;
          if (suggest.indexOf(v) < 0) suggest.push(v);
          mask.querySelector('#tag-new').value = '';
          draw(); refreshList();
        });
      };
      mask.querySelector('#tag-done').onclick = function () { mask.remove(); renderPreview(d); };
      mask.addEventListener('click', function (e) { if (e.target === mask) { mask.remove(); renderPreview(d); } });
    });
  }

  // ---------- 图片灯箱 ----------
  function openLightbox(d) {
    Core.request('get', { id: d.id }).then(function (full) {
      var lb = document.createElement('div');
      lb.className = 'lightbox';
      var img = document.createElement('img');
      img.src = full.image_full || full.image_thumb;
      lb.appendChild(img);
      lb.title = '点击关闭';
      lb.onclick = function () { lb.remove(); };
      document.body.appendChild(lb);
      var esc = function (e) { if (e.key === 'Escape') { lb.remove(); document.removeEventListener('keydown', esc); } };
      document.addEventListener('keydown', esc);
    });
  }

  function saveImage(d) {
    var name = 'clipvault-' + new Date(d.created_at).toISOString().slice(0, 10) + '-' + d.id + '.png';
    Core.pickSave(name, [{ name: '图片', extensions: ['png'] }]).then(function (path) {
      if (!path) return;
      return Core.request('save_image', { id: d.id, path: path }).then(function () {
        Core.toast('图片已保存');
      });
    }).catch(function (e) { if (e) Core.toast('保存失败：' + (e.message || e)); });
  }

  // ---------- 状态 ----------
  function toggleRecording() {
    Core.request('set_recording', { enabled: !state.recording }).then(function (r) {
      state.recording = r.recording;
      renderStatusPill();
      Core.toast(r.recording ? '已恢复记录' : '已暂停记录，复制的内容不会被保存');
    }).catch(function (e) { Core.toast('切换失败：' + e.message); });
  }

  function renderStatusPill() {
    statusPill.classList.toggle('paused', !state.recording);
    statusPill.classList.toggle('recording', state.recording);
    statusPill.querySelector('.txt').textContent = state.recording ? '记录中' : '已暂停';
    $('btn-toggle-rec').textContent = state.recording ? '暂停记录' : '恢复记录';
  }

  function refreshCounts() {
    Core.request('get_state').then(function (s) {
      state.counts = s;
      var mb = (s.image_bytes / 1048576).toFixed(1);
      statusCounts.innerHTML = '共 <b>' + s.entry_count + '</b> 条记录 · 文本 ' + s.text_count +
        ' · 图片 ' + s.image_count + '（' + mb + ' MB）';
    }).catch(function () {});
  }

  // ---------- 首次引导 ----------
  function maybeOnboard() {
    if (!state.settings || state.settings.onboarded) return;
    var steps = [
      { ico: '📋', title: '复制，就自动存好了', desc: '以后你复制的文字、截图，都会悄悄保存在这里。不用你做任何事，冲掉了也能找回来。' },
      { ico: '⌨️', title: '按 Win + Shift + V 快速找回', desc: '随时按下这组快捷键，会弹出一个小窗口，搜一下、点一下，内容就回到剪贴板了。' },
      { ico: '🔒', title: '所有数据只留在本机', desc: '没有账号、没有云同步、没有任何联网上传。你的复制内容，只有你自己能看到。' },
    ];
    var i = 0;
    var mask = document.createElement('div');
    mask.className = 'onboard-mask';
    document.body.appendChild(mask);
    function draw() {
      var s = steps[i];
      var dots = steps.map(function (_, k) {
        return '<span class="' + (k === i ? 'on' : '') + '"></span>';
      }).join('');
      mask.innerHTML = '<div class="onboard-card"><div class="step-ico">' + s.ico + '</div>' +
        '<h2>' + s.title + '</h2><p>' + s.desc + '</p>' +
        '<div class="onboard-dots">' + dots + '</div>' +
        '<button class="btn btn-primary" id="ob-next" style="min-width:180px">' +
        (i === steps.length - 1 ? '开始使用' : '下一步') + '</button></div>';
      mask.querySelector('#ob-next').onclick = function () {
        i++;
        if (i >= steps.length) { finish(); } else { draw(); }
      };
    }
    function finish() {
      mask.remove();
      Core.request('set_settings', { patch: { onboarded: true } }).catch(function () {});
    }
    draw();
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  document.addEventListener('DOMContentLoaded', init);
})();
