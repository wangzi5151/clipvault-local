/* popup.js — Win+Shift+V 弹出的快速面板：搜 → 点 → 复制 → 自动关闭 */
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };
  var listEl, searchInput, query = '', clickTimer = null;

  function init() {
    listEl = $('popup-list'); searchInput = $('search');
    if (Core.mock) {
      var b = document.createElement('div');
      b.className = 'mock-banner';
      b.textContent = '演示模式（无后端连接）';
      document.body.insertBefore(b, document.body.firstChild);
    }
    Core.applyTheme('auto');
    // 主题跟随主设置（尽力）
    Core.request('get_settings').then(function (s) {
      Core._theme = s.theme || 'auto'; Core.applyTheme(s.theme || 'auto');
    }).catch(function () {});

    var debounce = null;
    searchInput.addEventListener('input', function () {
      clearTimeout(debounce);
      debounce = setTimeout(function () { query = searchInput.value.trim(); load(); }, 180);
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') Core.closeWindow();
    });
    Core.onEvent(function (p) {
      if (p && (p.event === 'entry_added' || p.event === 'entry_deleted' || p.event === 'entries_cleared')) load();
    });
    load();
    searchInput.focus();
  }

  function load() {
    Core.request('list', { limit: 30, query: query }).then(function (r) {
      render(r.entries || []);
    }).catch(function (e) {
      listEl.innerHTML = '<div class="empty-tip">加载失败：' + escapeHtml(e.message) + '</div>';
    });
  }

  function render(entries) {
    if (!entries.length) {
      listEl.innerHTML = '<div class="empty-tip">' +
        (query ? '没找到，换个关键词试试。' : '还没有记录，去复制点什么吧。') + '</div>';
      return;
    }
    listEl.innerHTML = '';
    entries.forEach(function (e) {
      var div = document.createElement('div');
      div.className = 'popup-entry';
      var left = e.has_image
        ? '<img class="thumb" src="' + placeholderThumb() + '" alt="">'
        : '<div class="kind-ico">文</div>';
      div.innerHTML = left +
        '<div class="body"><div class="preview-line">' + escapeHtml(e.preview) + '</div>' +
        '<div class="meta"><span>' + Core.fmtTime(e.created_at) + '</span>' +
        (e.favorite ? '<span class="star" style="font-size:13px">★</span>' : '') + '</div></div>';
      // 单击（延迟判双击）= 复制并关闭；双击 = 打开主窗口对应条目
      div.addEventListener('click', function () {
        clearTimeout(clickTimer);
        clickTimer = setTimeout(function () { copyAndClose(e.id); }, 280);
      });
      div.addEventListener('dblclick', function () {
        clearTimeout(clickTimer);
        openInMain(e.id);
      });
      listEl.appendChild(div);
    });
  }

  function placeholderThumb() {
    return 'data:image/svg+xml;utf8,' + encodeURIComponent(
      '<svg xmlns="http://www.w3.org/2000/svg" width="44" height="44">' +
      '<rect width="44" height="44" fill="#2e3648"/>' +
      '<text x="22" y="28" font-size="16" text-anchor="middle" fill="#a7b0c5">图</text></svg>');
  }

  function copyAndClose(id) {
    Core.request('copy', { id: id }).then(function () {
      Core.closeWindow();
    }).catch(function (e) { Core.toast('复制失败：' + e.message); });
  }

  function openInMain(id) {
    // 通知主窗口选中该条目；窗口的显示/聚焦由 Rust 外壳负责
    Core.emitAppEvent('clipvault-open-entry', { id: id }).then(function () {
      Core.closeWindow();
    });
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  document.addEventListener('DOMContentLoaded', init);
})();
