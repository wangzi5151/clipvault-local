/* settings.js — 设置页：只做 IPC Settings 里的大类，每项配一句人话 */
(function () {
  'use strict';
  var $ = function (id) { return document.getElementById(id); };
  var S = null; // 当前设置

  function init() {
    if (Core.mock) {
      var b = document.createElement('div');
      b.className = 'mock-banner';
      b.textContent = '演示模式（无后端连接）· 改动只保存在本地演示数据';
      document.body.insertBefore(b, document.body.firstChild);
    }
    Core.request('get_settings').then(function (s) {
      S = s;
      Core._theme = s.theme || 'auto'; Core.applyTheme(s.theme || 'auto');
      bind();
    }).catch(function (e) {
      $('settings-wrap').innerHTML = '<div class="empty-tip">设置加载失败：' + escapeHtml(e.message) + '</div>';
    });
  }

  function save(patch, done) {
    Core.request('set_settings', { patch: patch }).then(function (s) {
      S = s;
      if (patch.theme !== undefined) { Core._theme = s.theme; Core.applyTheme(s.theme); }
      if (done) done();
      Core.toast('已保存');
    }).catch(function (e) { Core.toast('保存失败：' + e.message); });
  }

  function bind() {
    // —— 记录 ——
    $('set-autostart').checked = !!S.autostart;
    $('set-autostart').onchange = function (e) { save({ autostart: e.target.checked }); };

    $('set-max').value = S.max_entries;
    $('set-max').onchange = function (e) {
      var v = Math.max(50, Math.min(2000, parseInt(e.target.value, 10) || 200));
      e.target.value = v; save({ max_entries: v });
    };

    $('set-record-text').checked = S.record_text !== false;
    $('set-record-text').onchange = function (e) { save({ record_text: e.target.checked }); };

    $('set-record-images').checked = S.record_images !== false;
    $('set-record-images').onchange = function (e) { save({ record_images: e.target.checked }); };

    $('set-img-mb').value = S.image_space_mb;
    $('set-img-mb').onchange = function (e) {
      var v = Math.max(20, Math.min(2000, parseInt(e.target.value, 10) || 200));
      e.target.value = v; save({ image_space_mb: v });
    };

    // —— 隐私 ——
    setRadio('sens', S.skip_sensitive || 'ask');
    document.querySelectorAll('input[name="sens"]').forEach(function (r) {
      r.onchange = function () { save({ skip_sensitive: this.value }); };
    });

    setRadio('lock', S.lock_action || 'none');
    document.querySelectorAll('input[name="lock"]').forEach(function (r) {
      r.onchange = function () { save({ lock_action: this.value }); };
    });

    $('btn-clear-hour').onclick = function () {
      Core.ask('擦除最近一小时的记录？',
        '将删除最近 1 小时内的所有复制记录（收藏的也会被删除），这个操作无法撤销。',
        [{ id: 'cancel', text: '取消' }, { id: 'ok', text: '擦除', danger: true }])
        .then(function (c) {
          if (c !== 'ok') return;
          Core.request('clear_recent', { minutes: 60 }).then(function (r) {
            Core.toast('已擦除 ' + r.deleted + ' 条记录');
          }).catch(function (e) { Core.toast('擦除失败：' + e.message); });
        });
    };

    $('btn-clear-all').onclick = function () {
      Core.ask('清空全部历史记录？',
        '所有复制记录（含收藏和图片）都会被永久删除，无法恢复。建议先做一次备份。',
        [{ id: 'cancel', text: '取消' }, { id: 'backup', text: '先备份再清空', primary: true }, { id: 'ok', text: '直接清空', danger: true }],
        { dismissable: true })
        .then(function (c) {
          if (c === 'backup') { doBackup(true); return; }
          if (c !== 'ok') return;
          // 二次确认
          Core.ask('最后确认一次', '真的要清空全部记录吗？删除后无法找回。',
            [{ id: 'cancel', text: '我再想想' }, { id: 'ok', text: '确认清空', danger: true }])
            .then(function (c2) {
              if (c2 !== 'ok') return;
              Core.request('clear_all').then(function (r) {
                Core.toast('已清空 ' + r.deleted + ' 条记录');
              }).catch(function (e) { Core.toast('清空失败：' + e.message); });
            });
        });
    };

    // —— 备份与数据 ——
    $('btn-backup-now').onclick = function () { doBackup(false); };

    $('set-auto-backup').checked = S.auto_backup !== false;
    $('set-auto-backup').onchange = function (e) { save({ auto_backup: e.target.checked }); };

    $('set-dbdir').value = S.db_dir || '';
    $('dbdir-hint').textContent = '当前数据库位置：' + (S.db_dir || '默认（文档/ClipVault-Local 文件夹）');
    $('btn-dbdir').onclick = function () {
      Core.pickOpenDir().then(function (p) {
        if (!p) return;
        $('set-dbdir').value = p;
        save({ db_dir: p }, function () {
          $('dbdir-hint').textContent = '当前数据库位置：' + p + '（下次启动生效）';
        });
      });
    };
    $('btn-dbdir-reset').onclick = function () {
      $('set-dbdir').value = '';
      save({ db_dir: '' }, function () { $('dbdir-hint').textContent = '当前数据库位置：默认（文档/ClipVault-Local 文件夹）'; });
    };

    $('btn-export-json').onclick = function () {
      Core.pickOpenDir().then(function (dir) {
        if (!dir) return;
        Core.request('export_json', { dir: dir }).then(function (r) {
          Core.toast('已导出 ' + r.files + ' 个文件到所选文件夹');
        }).catch(function (e) { Core.toast('导出失败：' + e.message); });
      });
    };
    $('btn-export-bak').onclick = function () {
      var name = 'clipvault-backup-' + new Date().toISOString().slice(0, 10) + '.cvb';
      Core.pickSave(name, [{ name: '备份包', extensions: ['cvb'] }]).then(function (path) {
        if (!path) return;
        return Core.request('export_backup', { path: path }).then(function (r) {
          Core.toast('备份包已保存（' + Math.round(r.bytes / 1024) + ' KB）');
        });
      }).catch(function (e) { if (e) Core.toast('备份失败：' + (e.message || e)); });
    };
    $('btn-import-bak').onclick = function () {
      Core.pickOpenFile().then(function (path) {
        if (!path) return;
        Core.ask('导入备份？', '导入前会自动先备份当前数据，只认本软件自己导出的备份包。',
          [{ id: 'cancel', text: '取消' }, { id: 'ok', text: '开始导入', primary: true }])
          .then(function (c) {
            if (c !== 'ok') return;
            Core.request('import_backup', { path: path }).then(function (r) {
              Core.toast('导入完成：新增 ' + r.imported + ' 条，跳过 ' + r.skipped + ' 条');
            }).catch(function (e) { Core.toast('导入失败：' + e.message); });
          });
      });
    };

    // —— 数据库密码 ——
    renderPwdState();
    $('btn-set-pwd').onclick = function () {
      var p1 = $('pwd1').value, p2 = $('pwd2').value;
      if (!p1) { Core.toast('请先输入密码'); return; }
      if (p1 !== p2) { Core.toast('两次输入的密码不一致'); return; }
      Core.ask('给数据库加密码？',
        '加密码后，数据库文件会被加密保存。密码只有你自己知道——忘了就打不开了，我们也帮不了你。',
        [{ id: 'cancel', text: '取消' }, { id: 'ok', text: '确认加密', primary: true }])
        .then(function (c) {
          if (c !== 'ok') return;
          Core.request('set_db_password', { password: p1 }).then(function () {
            S.db_password_set = true; $('pwd1').value = ''; $('pwd2').value = '';
            renderPwdState(); Core.toast('数据库已加密');
          }).catch(function (e) { Core.toast('加密失败：' + e.message); });
        });
    };
    $('btn-clear-pwd').onclick = function () {
      Core.ask('移除数据库密码？', '移除后数据库将恢复为不加密保存。',
        [{ id: 'cancel', text: '取消' }, { id: 'ok', text: '确认移除', danger: true }])
        .then(function (c) {
          if (c !== 'ok') return;
          Core.request('set_db_password', { password: '' }).then(function () {
            S.db_password_set = false; renderPwdState(); Core.toast('密码已移除');
          }).catch(function (e) { Core.toast('操作失败：' + e.message); });
        });
    };

    // —— 快捷键 ——
    renderHotkey();
    $('btn-hotkey-rec').onclick = startHotkeyRecord;
    $('btn-hotkey-off').onclick = function () { save({ hotkey: '' }, renderHotkey); };

    // —— 主题 ——
    setRadio('theme', S.theme || 'auto');
    document.querySelectorAll('input[name="theme"]').forEach(function (r) {
      r.onchange = function () { save({ theme: this.value }); };
    });

    // —— 关于 ——
    Core.request('get_state').then(function (st) {
      $('about-ver').textContent = '版本 ' + (st.version || '未知');
      $('about-db').textContent = st.db_path || '';
    }).catch(function () {});
  }

  function renderPwdState() {
    var has = !!S.db_password_set;
    $('pwd-state').textContent = has ? '当前状态：已加密 🔒' : '当前状态：未加密（默认）';
    $('pwd-state').style.color = has ? 'var(--green)' : 'var(--text-faint)';
    $('btn-clear-pwd').style.display = has ? '' : 'none';
  }

  function renderHotkey() {
    $('hotkey-now').innerHTML = '当前：<span class="kbd">' + escapeHtml(Core.hotkeyToDisplay(S.hotkey)) + '</span>' +
      (S.hotkey ? '' : '（全局热键已关闭，可用托盘菜单调出）');
  }

  function startHotkeyRecord() {
    var btn = $('btn-hotkey-rec');
    btn.textContent = '请按下快捷键…（Esc 取消）';
    btn.classList.add('recording-flash');
    function handler(e) {
      e.preventDefault(); e.stopPropagation();
      if (e.key === 'Escape') { cleanup(); return; }
      var parts = [];
      if (e.ctrlKey) parts.push('Control');
      if (e.altKey) parts.push('Alt');
      if (e.shiftKey) parts.push('Shift');
      if (e.metaKey) parts.push('Super');
      var key = e.key.length === 1 ? e.key.toUpperCase() : e.key;
      if (['Control', 'Alt', 'Shift', 'Meta'].indexOf(e.key) < 0) parts.push(key);
      if (parts.length === 0) return;
      var hk = parts.join('+');
      cleanup();
      save({ hotkey: hk }, renderHotkey);
      Core.toast('快捷键已设为 ' + Core.hotkeyToDisplay(hk));
    }
    function cleanup() {
      document.removeEventListener('keydown', handler, true);
      btn.textContent = '重新录制';
      btn.classList.remove('recording-flash');
    }
    document.addEventListener('keydown', handler, true);
  }

  function doBackup(thenClear) {
    Core.request('auto_backup_now').then(function (r) {
      Core.toast('备份已保存：' + r.path);
      if (thenClear) {
        Core.request('clear_all').then(function (rr) { Core.toast('已清空 ' + rr.deleted + ' 条记录'); });
      }
    }).catch(function (e) { Core.toast('备份失败：' + e.message); });
  }

  function setRadio(name, val) {
    document.querySelectorAll('input[name="' + name + '"]').forEach(function (r) {
      r.checked = r.value === val;
    });
  }

  function escapeHtml(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  document.addEventListener('DOMContentLoaded', init);
})();
