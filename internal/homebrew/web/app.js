'use strict';
/* Alice Homebrew station UI. Values come from DESIGN.md tokens in style.css. */
const $ = id => document.getElementById(id);
let lang = localStorage.getItem('alice-language') === 'en' ? 'en' : 'ru';
const w = (ru, en) => lang === 'en' ? en : ru;
const pair = s => { const p = String(s || '').split('|'); return lang === 'en' ? (p[1] || p[0]) : p[0]; };
const text = x => typeof x === 'string' ? x : (x?.[lang] || x?.ru || x?.en || '');

let about = {}, state = null, yam = null, loadError = null;
let route = { page: 'plugins', id: '' };
const store = { q: '', source: 'all', mine: true };
const drafts = {};
let tabs = null, lastYamBusy = false;

/* ── element helpers ─────────────────────────────────────────── */
function h(tag, props, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (k === 'data') { for (const [dk, dv] of Object.entries(v)) if (dv != null && dv !== false) e.dataset[dk] = dv === true ? '' : dv; }
    else if (k === 'style') { for (const [sk, sv] of Object.entries(v)) e.style.setProperty(sk, sv); }
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) if (kid != null && kid !== false) e.append(kid.nodeType ? kid : String(kid));
  return e;
}
const ICONS = {
  search: 'M10.5 10.5L14 14M6.75 12a5.25 5.25 0 1 0 0-10.5 5.25 5.25 0 0 0 0 10.5z',
  plus: 'M8 2.5v11M2.5 8h11',
  refresh: 'M13.5 8A5.5 5.5 0 1 1 11.6 3.8M13.5 1.8v3h-3',
  back: 'M10 3L5 8l5 5',
  download: 'M8 2v8.5M4.5 7L8 10.5 11.5 7M2.5 13.5h11',
  box: 'M2.5 5L8 2l5.5 3v6L8 14l-5.5-3zM2.5 5L8 8l5.5-3M8 8v6',
  ring: 'M8 2.2v.1M11.9 3.8v.1M13.8 8h-.1M11.9 12.1v.1M8 13.8v-.1M4.1 12.1v.1M2.2 8h.1M4.1 3.8v.1',
  wave: 'M2 8h1M4.5 5.5v5M7 3v10M9.5 5v6M12 6.5v3M14 8h-.5',
  term: 'M2.5 3h11v10h-11zM5 6.5L7 8l-2 1.5M8.5 10h2.5',
  link: 'M9.5 2.5h4v4M13.5 2.5L7.5 8.5M11.5 9.5v4h-9v-9h4',
  station: 'M4 6.5a4 4 0 0 1 8 0v6.5H4zM6.5 9.5h3'
};
function icon(name) {
  const ns = 'http://www.w3.org/2000/svg', s = document.createElementNS(ns, 'svg'), p = document.createElementNS(ns, 'path');
  for (const [k, v] of Object.entries({ viewBox: '0 0 16 16', 'aria-hidden': 'true', fill: 'none', stroke: 'currentColor', 'stroke-width': name === 'ring' ? '2.4' : '1.5', 'stroke-linecap': 'round', 'stroke-linejoin': 'round' })) s.setAttribute(k, v);
  p.setAttribute('d', ICONS[name]); s.append(p); return s;
}
/* One button component for the whole UI: kit press + pending/done state for async work. */
function button(label, fn, variant = 'secondary', opts = {}) {
  const b = h('button', { type: opts.submit ? 'submit' : 'button', class: `btn btn--${variant}${opts.small ? ' btn--sm' : ''} ui-press ui-focus${variant === 'primary' ? '' : ' ui-press--on-dark'}`, 'aria-label': opts.aria, title: opts.aria });
  b.append(h('span', { class: 'ui-btn-state' },
    h('span', { class: 'ui-state-label' }, opts.icon ? icon(opts.icon) : null, label ? h('span', {}, label) : null),
    h('span', { class: 'ui-state-pending', 'aria-hidden': 'true' }, h('i', { class: 'ui-spinner' })),
    h('span', { class: 'ui-state-done', 'aria-hidden': 'true' }, '✓')));
  if (opts.disabled) b.disabled = true;
  if (fn) b.addEventListener('click', () => perform(b, fn));
  return b;
}
function perform(b, fn) {
  let result;
  try { result = fn(); } catch (e) { report(e); return; }
  if (result && typeof result.then === 'function') {
    if (b && b.isConnected) BetterUI.runWithState(b, () => result, { doneMs: 900 }).catch(report);
    else result.catch(report);
  }
}
function report(e) { if (e && !e.silent) toast(errorText(e), 'danger'); }
function tag(label, tone) { return h('span', { class: 'tag', data: { tone } }, label); }
function status(label, tone) { return h('span', { class: 'status', data: { tone } }, label); }
function callout(tone, title, body, ...actions) {
  const c = h('div', { class: 'callout', data: { tone } }, h('strong', { class: 't-h3' }, title));
  if (body) c.append(h('p', { class: 'muted' }, body));
  if (actions.length) c.append(h('div', { class: 'button-row' }, actions));
  return c;
}
function checkbox(label, onchange) {
  const box = h('button', { type: 'button', role: 'checkbox', 'aria-checked': 'false', class: 'ui-check ui-focus' });
  const ns = 'http://www.w3.org/2000/svg', svg = document.createElementNS(ns, 'svg'), path = document.createElementNS(ns, 'path');
  svg.setAttribute('viewBox', '0 0 14 14'); svg.setAttribute('aria-hidden', 'true'); path.setAttribute('d', 'M2.5 7.5l3 3 6-7'); svg.append(path); box.append(svg);
  const id = 'c' + Math.random().toString(36).slice(2, 9), lab = h('label', { id, class: 't-small' }, label);
  box.setAttribute('aria-labelledby', id);
  const row = h('div', { class: 'check-row' }, box, lab);
  const toggle = () => { box.setAttribute('aria-checked', String(box.getAttribute('aria-checked') !== 'true')); onchange?.(); };
  box.addEventListener('click', toggle); lab.addEventListener('click', toggle);
  return { row, get checked() { return box.getAttribute('aria-checked') === 'true'; } };
}
function switchControl(on, label, fn) {
  const s = h('button', { type: 'button', role: 'switch', 'aria-checked': String(!!on), class: 'ui-switch ui-focus', 'aria-label': label });
  s.addEventListener('click', () => perform(null, () => fn(s)));
  return s;
}
function formatBytes(value) {
  if (!Number.isFinite(Number(value))) return '—';
  let n = Number(value), i = 0; const units = lang === 'ru' ? ['Б', 'КиБ', 'МиБ', 'ГиБ'] : ['B', 'KiB', 'MiB', 'GiB'];
  while (n >= 1024 && i < 3) { n /= 1024; i++; }
  return `${n.toLocaleString(lang === 'ru' ? 'ru-RU' : 'en-US', { maximumFractionDigits: i ? 1 : 0 })} ${units[i]}`;
}
const formatDate = v => v && !String(v).startsWith('0001-') ? new Date(v).toLocaleString(lang === 'ru' ? 'ru-RU' : 'en-US', { dateStyle: 'short', timeStyle: 'short' }) : '—';

/* Server errors are English; the common ones get a translation, the rest stay as sent. */
const ERRORS = {
  'another yam operation is still running': 'Другая операция yam ещё выполняется',
  'package confirmation expired': 'Подтверждение устарело — откройте пакет снова',
  'confirm every requested permission': 'Отметьте все разрешения',
  'package has no build for this station': 'Для этой станции нет сборки пакета',
  'package is not in the loaded yam registry; refresh the catalog': 'Пакета нет в загруженном реестре — обновите каталог',
  'yam v2 cannot be installed here': 'yam v2 нельзя установить здесь',
  'not enough storage; select plugins to remove and retry': 'Недостаточно места',
  'too many pending package confirmations': 'Слишком много неподтверждённых пакетов — подождите 10 минут или подтвердите открытые'
};
function errorText(e) {
  const m = String(e?.message || e || '');
  if (lang === 'ru' && ERRORS[m]) return ERRORS[m];
  if (lang === 'ru' && m.startsWith('plugin is not compatible with')) return 'Плагин не подходит этой станции: ' + m.split(': ').slice(1).join(': ');
  if (lang === 'ru' && /yam (v1|v2) is not available/.test(m)) return 'yam не найден на станции';
  return m || w('Неизвестная ошибка', 'Unknown error');
}
function toast(message, tone = '') {
  const t = h('div', { class: 'toast', data: { tone } }, h('span', {}, message));
  $('toasts').append(t);
  setTimeout(() => { t.dataset.leaving = ''; setTimeout(() => t.remove(), 200); }, tone === 'danger' ? 8000 : 4000);
}
async function api(path, data, raw = false) {
  const options = { credentials: 'same-origin' };
  if (data !== undefined) { options.method = 'POST'; options.headers = { 'Content-Type': raw ? 'application/octet-stream' : 'application/json' }; options.body = raw ? data : JSON.stringify(data); }
  const response = await fetch(path, options);
  let result; try { result = await response.json(); } catch { throw Error(`HTTP ${response.status}`); }
  if (!response.ok) { const error = Error(result.error || `HTTP ${response.status}`); error.code = result.code; error.details = result.details; throw error; }
  return result;
}

/* ── dialog ──────────────────────────────────────────────────── */
let dialogTrigger = null;
function openDialog(...children) {
  const d = $('dialog');
  $('dialog-body').replaceChildren(h('div', { class: 'dialog-inner' }, children));
  if (!d.open) { dialogTrigger = document.activeElement; d.showModal(); if (dialogTrigger?.getBoundingClientRect) BetterUI.originFrom(d, dialogTrigger); }
  const first = d.querySelector('input, select, textarea, .ui-check');
  (first || d.querySelector('.btn'))?.focus();
}
function closeDialog() { const d = $('dialog'); if (d.open) d.close(); }
$('dialog').addEventListener('close', () => { if (dialogTrigger?.isConnected) dialogTrigger.focus(); });
function ask({ title, body, confirm, tone = 'primary', extra }) {
  return new Promise(resolve => {
    let done = false; const finish = v => { if (!done) { done = true; resolve(v); } };
    $('dialog').addEventListener('close', () => finish(false), { once: true });
    openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, title), body ? h('p', { class: 'muted' }, body) : null, extra || null,
      h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), button(confirm, () => { finish(true); closeDialog(); }, tone === 'danger' ? 'secondary' : 'primary')));
  });
}

/* ── device and compatibility ────────────────────────────────── */
const device = () => state?.device || {};
function compat(m) {
  const d = device();
  if (!d.known) return {};
  if (m.devices?.length && !m.devices.includes(d.id)) return { block: w('Плагин собран для других станций', 'Built for other stations') };
  if (m.engine === 'native' && d.arch !== 'aarch64') return { block: w('Нативным плагинам нужна станция ARM64', 'Native plugins need an ARM64 station') };
  if (m.permissions?.includes('led.ring') && !d.led_ring) return { note: w('Драйвер кольца подсветки проверен только на Мини 1. Здесь плагин работает, но светодиоды не изменятся.', 'The LED ring driver is verified on Station Mini 1 only. The plugin runs here, but the LEDs will not change.') };
  return {};
}
const SOURCE_TONE = { v2: 'info', v1: '', homebrew: 'signal' };
function sourceTag(kind, entry) {
  if (kind === 'v2') return tag('yam v2', 'info');
  if (kind === 'v1') return tag('yam v1', '');
  if (entry && !entry.source_id && !entry.source_official && entry.reviewed === false) return tag(w('локальный пакет', 'local package'), '');
  return entry?.source_official === false ? tag(w('сторонний источник', 'community source'), '') : null;
}

/* ── LED ring (brand mark and live preview) ──────────────────── */
/* The preview draws the LEDs this station really has (state.ring.layout):
   Mini 1 has 4 under the logo plus 8 around, Midi 24, Mini 2 six, Lite three. */
function ringLayout() { return state?.ring?.layout || { id: 'mini1', count: 12, center: 4 }; }
function ringElement(large, pluginId) {
  const l = ringLayout(), r = h('span', { class: 'ring-mark' + (large ? ' ring-large' : ''), 'aria-hidden': 'true', data: { plugin: pluginId, layout: l.id, count: String(l.count) } });
  const c = large ? 84 : 16, rad = large ? 68 : 12.5, inner = large ? 13 : 3.5, center = l.center || 0, outer = l.count - center;
  for (let i = 0; i < center; i++) {
    const x = i % 2 ? 1 : -1, y = i < 2 ? -1 : 1;
    r.append(h('i', { class: 'led-center', style: { left: `${c + x * inner}px`, top: `${c + y * inner}px` } }));
  }
  for (let i = 0; i < outer; i++) {
    const a = (i / outer) * Math.PI * 2 - Math.PI / 2;
    r.append(h('i', { class: outer > 12 ? 'led-dense' : null, style: { left: `${c + Math.cos(a) * rad}px`, top: `${c + Math.sin(a) * rad}px` } }));
  }
  return r;
}
function updateRings() {
  const colors = state?.ring?.leds || [], l = ringLayout();
  let replaced = false;
  for (const ring of document.querySelectorAll('.ring-mark')) {
    if (ring.dataset.count !== String(l.count)) { const fresh = ringElement(ring.classList.contains('ring-large'), ring.dataset.plugin); if (ring.id) fresh.id = ring.id; ring.replaceWith(fresh); replaced = true; continue; }
    const id = ring.dataset.plugin, p = id && state?.plugins?.find(p => p.manifest.id === id), active = !id || p?.enabled;
    [...ring.children].forEach((dot, i) => {
      const color = active ? colors[i] : null, lit = color && color !== '#000000';
      if (lit) { dot.dataset.lit = ''; dot.style.setProperty('--led', color); } else { delete dot.dataset.lit; dot.style.removeProperty('--led'); }
    });
  }
  if (replaced) updateRings();
}
const ringNeeded = () => state?.plugins?.some(p => p.enabled && p.manifest.permissions.includes('led.ring'));

/* ── routing ─────────────────────────────────────────────────── */
const PAGES = ['plugins', 'store', 'station'];
function parseRoute() {
  const parts = location.hash.replace(/^#\/?/, '').split('/');
  route = { page: PAGES.includes(parts[0]) ? parts[0] : 'plugins', id: decodeURIComponent(parts.slice(1).join('/')) };
}
function go(page, id = '') {
  const hash = '#/' + page + (id ? '/' + encodeURIComponent(id) : '');
  if (location.hash !== hash) location.hash = hash; else render();
}
window.addEventListener('hashchange', () => { parseRoute(); render(true); });

function buildChrome() {
  const nav = $('tabs');
  nav.setAttribute('role', 'tablist');
  nav.replaceChildren(...PAGES.map(p => h('button', { type: 'button', role: 'tab', id: 'tab-' + p, 'aria-selected': String(route.page === p), class: 'ui-focus' }, '')));
  tabs = BetterUI.segmented(nav, { onChange: i => { if (route.page !== PAGES[i]) go(PAGES[i]); } });
  for (const b of $('lang').querySelectorAll('button')) b.addEventListener('click', () => {
    lang = b.dataset.lang; localStorage.setItem('alice-language', lang);
    for (const x of $('lang').querySelectorAll('button')) x.setAttribute('aria-pressed', String(x === b));
    render();
  });
  BetterUI.segmented($('lang'));
  $('brand-ring').replaceWith(Object.assign(ringElement(false), { id: 'brand-ring' }));
  BetterUI.init(document);
}
function chrome() {
  document.documentElement.lang = lang;
  const labels = { plugins: w('Плагины', 'Plugins'), store: w('Магазин', 'Store'), station: w('Станция', 'Station') };
  PAGES.forEach(p => { const b = $('tab-' + p); b.textContent = labels[p]; b.setAttribute('aria-selected', String(route.page === p)); b.tabIndex = route.page === p ? 0 : -1; });
  $('content').setAttribute('role', 'tabpanel'); $('content').setAttribute('aria-labelledby', 'tab-' + route.page);
  if (tabs) tabs.select(PAGES.indexOf(route.page));
  for (const x of $('lang').querySelectorAll('button')) x.setAttribute('aria-pressed', String(x.dataset.lang === lang));
  $('skip').textContent = w('К содержимому', 'Skip to content');
  $('tabs').setAttribute('aria-label', w('Разделы', 'Sections'));
  $('drop-veil-text').textContent = w('Отпустите, чтобы проверить пакет', 'Release to check the package');
  const d = device(), dev = $('device');
  dev.replaceChildren();
  if (d.known) dev.append(h('span', {}, pair(d.name)));
}

function meter(used, total) {
  const pct = total ? Math.min(100, 100 * used / total) : 0;
  return h('span', { class: 'meter', data: { high: pct > 85 }, role: 'presentation' }, h('span', { style: { width: pct.toFixed(1) + '%' } }));
}
function jobLabel(job) {
  const verb = { install: w('ставит', 'installing'), 'install-local': w('ставит', 'installing'), remove: w('удаляет', 'removing'), setup: w('устанавливается', 'setting up') }[job.action] || job.action;
  return job.action === 'setup' ? `yam ${job.registry} ${verb}` : `${verb} ${job.name}`;
}
/* Progress of the yam runtimes Homebrew installs after an update: shown at the
   top of the plugin and store lists while running, and after a failure. */
function hiddenJobs() { try { return JSON.parse(localStorage.getItem('alice-hidden-jobs') || '[]'); } catch { return []; } }
function setupProgress() {
  const out = h('div', { class: 'pane-tools setup-progress', 'data-setup-progress': '' });
  for (const reg of ['v2', 'v1']) {
    const job = yam?.jobs?.find(j => j.action === 'setup' && j.registry === reg), name = reg === 'v2' ? 'yam v2' : 'yam v1 (yamd)';
    if (!job || hiddenJobs().includes(job.id)) continue;
    if (job.state === 'done' && yamRuntime(reg).available) { out.append(h('div', { class: 'setup-row setup-done' }, status(w(`${name} установлен`, `${name} installed`), 'ok'), button(w('Скрыть', 'Hide'), () => { localStorage.setItem('alice-hidden-jobs', JSON.stringify([...hiddenJobs(), job.id].slice(-16))); render(); }, 'ghost', { small: true }))); continue; }
    if (yamRuntime(reg).available) continue;
    if (job.state === 'running') out.append(h('div', { class: 'setup-row' }, status(w(`Устанавливаем ${name}`, `Installing ${name}`), 'busy'), h('span', { class: 't-small muted' }, w('скачиваем с GitHub и сверяем SHA-256', 'downloading from GitHub and checking SHA-256'))));
    else if (job.state === 'error') out.append(callout('danger', w(`${name} не установился`, `${name} was not installed`), (job.output || '').trim().split('\n').pop(), button(w('Повторить', 'Retry'), () => yamJob('setup', reg, ''), 'secondary', { small: true, disabled: yam?.busy })));
  }
  return out.childElementCount ? out : null;
}
function updateWord(s) {
  return ({ idle: w('ожидает проверки', 'waiting'), checking: w('проверяем…', 'checking…'), downloading: w('скачиваем…', 'downloading…'), restarting: w('перезапуск…', 'restarting…'), 'up-to-date': w('актуален', 'up to date'), updated: w('обновлён', 'updated'), available: w('есть обновление', 'update available'), error: w('ошибка обновления', 'update failed'), 'rolled-back': w('откат', 'rolled back') })[s] || '';
}

/* ── layout ──────────────────────────────────────────────────── */
function split(listPane, detailPane, hasDetail) {
  return h('div', { class: 'split', data: { hasDetail, uiEdge: true } }, listPane, h('section', { class: 'detail-pane', 'aria-live': 'polite' }, detailPane));
}
function backButton(page) { const b = button(w('Назад к списку', 'Back to list'), () => go(page), 'ghost', { small: true, icon: 'back' }); b.classList.add('back'); return b; }
function detailWrap(enter, ...children) { return h('article', { class: 'detail', data: { enter } }, children); }
function skeletonList() { return h('div', {}, [1, 2, 3, 4].map(() => h('div', { class: 'skeleton', 'aria-hidden': 'true' }, h('i'), h('b')))); }
function listRow({ key, page, title, sub, side, dim, current, control }) {
  return h('li', { class: 'row-wrap', 'aria-current': current ? 'true' : null }, h('button', { type: 'button', class: 'row ui-press ui-press--surface ui-press--on-dark ui-focus', 'aria-current': current ? 'true' : null, data: { dim, uiAlign: 'start' }, onclick: () => go(page, key) },
    h('span', { class: 'row-main' }, h('span', { class: 'row-title' }, title), sub ? h('span', { class: 'row-sub' }, sub) : null), h('span', { class: 'row-side' }, side)), control || null);
}

/* ── plugins page ────────────────────────────────────────────── */
function installedItems() {
  const items = [];
  for (const p of state?.plugins || []) items.push({ key: 'hb:' + p.manifest.id, kind: 'homebrew', plugin: p });
  for (const reg of ['v2', 'v1']) for (const [name, version] of Object.entries(yam?.installed?.[reg] || {})) {
    const entry = yam.registries.find(r => r.id === reg)?.packages.find(x => x.name === name);
    items.push({ key: reg + ':' + name, kind: reg, name, version, entry });
  }
  return items;
}
function pluginsPage(enter) {
  const items = installedItems(), wide = matchMedia('(min-width: 961px)').matches;
  let selected = items.find(i => i.key === route.id);
  if (!selected && route.id && state) { go('plugins'); return null; }
  if (!selected && wide) selected = items[0];
  const list = h('div', { class: 'list-pane' }, h('div', { class: 'pane-head' }, h('h1', { class: 't-h2' }, w('Плагины', 'Plugins')), button(w('Добавить', 'Add'), () => addDialog(), 'secondary', { small: true, icon: 'plus' })), setupProgress());
  if (!state) list.append(loadError ? h('div', { class: 'empty' }, h('p', { class: 't-h3' }, w('Нет связи с менеджером', 'Cannot reach the manager')), h('p', {}, errorText(loadError)), button(w('Повторить', 'Retry'), () => load(), 'primary')) : skeletonList());
  else if (!items.length) list.append(h('div', { class: 'empty' }, h('p', { class: 't-h3' }, w('Пока ничего не установлено', 'Nothing installed yet')), h('p', {}, w('В магазине — плагины Homebrew и пакеты yam для вашей станции.', 'The store has Homebrew plugins and yam packages for your station.')), h('div', { class: 'button-row' }, button(w('Открыть магазин', 'Open the store'), () => go('store'), 'primary'), button(w('Добавить пакет', 'Add a package'), () => addDialog(), 'secondary'))));
  else {
    const groups = [['homebrew', w('Плагины', 'Plugins')], ['v2', w('Пакеты yam v2', 'yam v2 packages')], ['v1', w('Пакеты yam v1', 'yam v1 packages')]];
    const kinds = new Set(items.map(i => i.kind));
    for (const [kind, label] of groups) {
      const group = items.filter(i => i.kind === kind);
      if (!group.length) continue;
      if (kinds.size > 1) list.append(h('div', { class: 'group-label' }, h('span', {}, label)));
      list.append(h('ul', { class: 'list' }, group.map(i => i.kind === 'homebrew' ? pluginRow(i, selected) : listRow({ key: i.key, page: 'plugins', title: i.name, sub: i.entry?.summary, side: h('span', { class: 'version' }, i.version), current: selected?.key === i.key }))));
    }
  }
  let detail;
  if (!state) detail = null;
  else if (!selected) detail = stationIntro(enter);
  else if (selected.kind === 'homebrew') detail = pluginDetail(selected.plugin, enter);
  else detail = yamInstalledDetail(selected, enter);
  return split(list, detail, !!route.id);
}
function pluginRow(i, selected) {
  const p = i.plugin, m = p.manifest;
  const sw = pluginSwitch(p);
  const row = listRow({ key: i.key, page: 'plugins', title: text(m.title), sub: text(m.description), side: rowNotes(p), current: selected?.key === i.key, control: sw });
  row.querySelector('.row-side').dataset.notes = m.id;
  return row;
}
function rowNotes(p) {
  const out = [];
  if (p.last_error) out.push(status(w('Ошибка', 'Error'), 'danger'));
  if (p.update) out.push(tag(w('обновление', 'update'), 'signal'));
  return out;
}
function pluginSwitch(p) {
  const sw = switchControl(p.enabled, (p.enabled ? w('Выключить ', 'Turn off ') : w('Включить ', 'Turn on ')) + text(p.manifest.title), () => togglePlugin(p));
  sw.dataset.pluginToggle = p.manifest.id;
  return sw;
}
function stationIntro(enter) {
  const d = device();
  return detailWrap(enter, h('div', { class: 'block' }, ringElement(true),
    h('h2', { class: 't-title' }, pair(d.name) || 'Alice Homebrew'),
    !d.led_ring && d.known ? h('p', { class: 'muted' }, w('Подсветка этой станции пока не поддерживается.', 'LEDs on this station are not supported yet.')) : null));
}
function pluginDetail(p, enter) {
  const m = p.manifest, id = m.id, c = compat(m);
  const sw = pluginSwitch(p);
  const actions = h('div', { class: 'detail-actions' });
  if (p.update) actions.append(button(w('Обновить до ', 'Update to ') + p.update.manifest.version, () => updatePlugin(p), 'primary'));
  const head = h('header', { class: 'detail-head' }, h('div', { class: 'title-row' }, h('h2', { class: 't-title', id: 'detail-title' }, text(m.title)), sw, h('label', { class: 'switch-word', 'data-switch-label': id, onclick: () => sw.click() }, p.enabled ? w('включён', 'on') : w('выключен', 'off'))),
    h('div', { class: 'byline' }, sourceTag('homebrew', { source_official: p.source_official, source_id: p.source_id, reviewed: p.reviewed }), h('span', { class: 'version' }, 'v' + m.version), p.source_official ? null : h('span', {}, m.author)), actions);
  const out = detailWrap(enter, backButton('plugins'), head);
  if (text(m.description)) out.append(h('p', { class: 'lede' }, text(m.description)));
  const err = callout('danger', w('Плагин остановлен с ошибкой', 'The plugin stopped with an error'), p.last_error || '');
  err.dataset.pluginError = id; err.hidden = !p.last_error; out.append(err);
  if (c.note) out.append(callout('warn', w('Ограничение этой станции', 'Limitation on this station'), c.note));
  const cols = h('div', { class: 'cols' });
  cols.append(m.config?.length ? configForm(p) : h('div', { class: 'block' }, h('h3', { class: 't-h3' }, w('Настройки', 'Settings')), h('p', { class: 'muted' }, w('У плагина нет настроек.', 'This plugin has no settings.'))));
  cols.append(pluginPanel(p));
  out.append(cols);
  const journal = h('pre', { class: 'log', 'data-plugin-logs': id }, logText(p));
  out.append(h('section', { class: 'block' }, h('h3', { class: 't-h3' }, w('Журнал', 'Log')), journal));
  const manage = h('section', { class: 'block' }, h('h3', { class: 't-h3' }, w('Управление', 'Manage')),
    h('dl', { class: 'facts' }, h('dt', {}, w('На диске', 'On disk')), h('dd', {}, formatBytes(p.storage_bytes || 0) + (p.previous_bytes ? ' · ' + w('версия для отката ', 'rollback copy ') + formatBytes(p.previous_bytes) : '')),
      m.engine !== 'native' ? [h('dt', {}, w('Память WASM', 'WASM memory')), h('dd', {}, formatBytes(p.wasm_memory_bytes || 0))] : [h('dt', {}, w('Движок', 'Engine')), h('dd', {}, w('нативный, полный доступ root', 'native, full root access'))],
      h('dt', {}, 'SHA-256'), h('dd', { class: 'hash' }, p.sha256)));
  const row = h('div', { class: 'button-row' }, button(w('Скачать конфиг', 'Download config'), () => downloadConfig(p), 'secondary', { small: true, icon: 'download' }));
  if (p.previous) row.append(button(w('Вернуть версию ', 'Roll back to ') + p.previous.manifest.version, async () => {
    if (!await ask({ title: w('Вернуть предыдущую версию?', 'Roll back?'), body: w(`${text(m.title)} вернётся к версии ${p.previous.manifest.version} вместе с её настройками.`, `${text(m.title)} returns to ${p.previous.manifest.version} with its settings.`), confirm: w('Вернуть', 'Roll back') })) return;
    await api(`/api/plugins/${id}/rollback`, { permissions: p.previous.manifest.permissions }); await load(); toast(w('Версия возвращена', 'Version restored'), 'ok');
  }, 'secondary', { small: true }));
  row.append(button(w('Удалить', 'Remove'), async () => {
    if (!await ask({ title: w(`Удалить ${text(m.title)}?`, `Remove ${text(m.title)}?`), body: w('Плагин, его настройки и данные будут удалены с колонки.', 'The plugin, its settings and data are deleted from the speaker.'), confirm: w('Удалить', 'Remove'), tone: 'danger' })) return;
    await api(`/api/plugins/${id}/remove`, {}); delete drafts[id]; await load(); go('plugins'); toast(w('Плагин удалён', 'Plugin removed'));
  }, 'danger', { small: true }));
  manage.append(row); out.append(manage);
  return out;
}
const logText = p => p.logs?.length ? p.logs.join('\n') : w('Записей пока нет. Журнал появится после запуска.', 'No entries yet. The log appears after startup.');
function downloadConfig(p) {
  const blob = new Blob([JSON.stringify({ plugin_id: p.manifest.id, version: p.manifest.version, values: p.config }, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob), a = h('a', { href: url, download: p.manifest.id + '.config.json' }); a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}
function configForm(p) {
  const m = p.manifest, values = drafts[m.id] || (drafts[m.id] = { ...p.config });
  const form = h('form', { class: 'block', novalidate: true }, h('h3', { class: 't-h3' }, w('Настройки', 'Settings')));
  const changed = () => form.dataset.dirty = '';
  for (const f of m.config) {
    const id = `config-${m.id}-${f.key}`, field = h('div', { class: 'field' }, h('label', { for: id }, text(f.label))), row = h('div', { class: 'field-row' });
    if (f.type === 'range') {
      const range = h('input', { type: 'range', id, min: f.min ?? 0, max: f.max ?? 0, step: f.step, value: values[f.key] });
      const exact = h('input', { type: 'number', class: 'input input-exact ui-field', min: f.min ?? 0, max: f.max ?? 0, step: f.step, value: values[f.key], required: true, 'aria-label': text(f.label) + ' · ' + w('точное значение', 'exact value') });
      range.oninput = () => { exact.value = range.value; values[f.key] = Number(range.value); changed(); };
      exact.oninput = () => { if (exact.checkValidity()) { range.value = exact.value; values[f.key] = exact.valueAsNumber; changed(); } };
      row.append(range, exact);
    } else if (f.type === 'color') {
      const color = h('input', { type: 'color', id, value: values[f.key] });
      const hex = h('input', { type: 'text', class: 'input input-exact ui-field t-mono', pattern: '#[0-9a-fA-F]{6}', maxlength: 7, value: values[f.key], required: true, 'aria-label': text(f.label) + ' HEX' });
      color.oninput = () => { hex.value = color.value; values[f.key] = color.value; changed(); };
      hex.oninput = () => { if (hex.checkValidity()) { color.value = hex.value; values[f.key] = hex.value.toLowerCase(); changed(); } };
      row.append(color, hex);
    } else if (f.type === 'boolean') {
      const sw = h('button', { type: 'button', role: 'switch', id, 'aria-checked': String(!!values[f.key]), class: 'ui-switch ui-focus' });
      sw.onclick = () => { values[f.key] = sw.getAttribute('aria-checked') !== 'true'; sw.setAttribute('aria-checked', String(values[f.key])); changed(); };
      row.append(sw);
    } else if (f.type === 'select') {
      const sel = h('select', { id, class: 'input ui-field' }, f.options.map(o => h('option', { value: o.value }, text(o.label))));
      sel.value = values[f.key]; sel.onchange = () => { values[f.key] = sel.value; changed(); }; row.append(sel);
    } else {
      const input = h('input', { type: 'text', id, class: 'input ui-field', maxlength: f.max_length, value: values[f.key] ?? '' });
      input.oninput = () => { values[f.key] = input.value; changed(); }; row.append(input);
    }
    field.append(row); form.append(field);
  }
  const save = button(w('Сохранить настройки', 'Save settings'), null, 'primary', { submit: true });
  form.append(h('div', { class: 'button-row' }, save));
  form.addEventListener('submit', e => {
    e.preventDefault();
    const bad = form.querySelector(':invalid');
    if (bad) { BetterUI.shake(bad); bad.focus(); toast(w('Проверьте значение поля', 'Check the field value'), 'danger'); return; }
    perform(save, async () => { await api(`/api/plugins/${m.id}/config`, { values }); delete drafts[m.id]; await load(); toast(w('Настройки сохранены', 'Settings saved'), 'ok'); });
  });
  return form;
}
function pluginPanel(p) {
  const m = p.manifest, panel = h('aside', { class: 'block', 'data-plugin-panel': m.id });
  if (m.permissions.includes('led.ring')) {
    panel.append(ringElement(true, m.id), h('h3', { class: 't-h3' }, w('Подсветка сейчас', 'LEDs now')));
  } else if (m.id === 'music-ring' || m.id === 'airplay') {
    panel.dataset.audioPanel = m.id;
    panel.append(h('h3', { class: 't-h3' }, m.id === 'airplay' ? w('Приёмник AirPlay', 'AirPlay receiver') : w('Музыкальное кольцо', 'Music ring')));
    if (m.id === 'airplay') panel.append(h('dl', { class: 'facts' }, h('dt', {}, w('Имя', 'Name')), h('dd', {}, String(p.config.name || text(m.title))), h('dt', {}, w('Порт', 'Port')), h('dd', { class: 't-mono' }, String(p.config.port ?? '—')), h('dt', {}, w('Пароль', 'Password')), h('dd', {}, p.config.password ? w('включён', 'on') : w('нет', 'none'))));
    panel.append(h('div', { class: 'spectrum', role: 'img', 'aria-label': w('Уровни музыки', 'Music levels') }, Array.from({ length: 12 }, () => h('i'))), h('p', { class: 'muted t-small', 'data-audio-note': m.id }));
  } else {
    panel.append(h('h3', { class: 't-h3' }, w('Возможности', 'Capabilities')));
    if (m.capabilities?.length) panel.append(h('div', { class: 'chips' }, m.capabilities.map(cap => tag(cap))));
    else panel.append(h('p', { class: 'muted t-small' }, m.permissions.map(permissionLabel).join(', ') || '—'));
  }
  return panel;
}
function updatePanels() {
  for (const panel of document.querySelectorAll('[data-audio-panel]')) {
    const id = panel.dataset.audioPanel, p = state?.plugins?.find(x => x.manifest.id === id); if (!p) continue;
    const data = state.audio, live = !!p.enabled && data?.available && data.source === 'airplay';
    panel.querySelectorAll('.spectrum i').forEach((bar, i) => { const level = live ? Math.min(1, Math.sqrt((data.bands?.[i] || 0) * 12)) : 0; bar.style.height = `${5 + level * 95}%`; if (live && data.active) bar.dataset.on = ''; else delete bar.dataset.on; });
    const note = panel.querySelector('[data-audio-note]');
    if (note) note.textContent = !p.enabled ? w('Включите плагин, чтобы начать.', 'Enable the plugin to begin.') : data?.error ? data.error : live ? (data.active ? w('Звук поступает из AirPlay', 'Receiving AirPlay audio') : w('Поток AirPlay: тишина', 'AirPlay stream: silence')) : w('Ожидаем музыку: выберите колонку в AirPlay на iPhone.', 'Waiting for music: pick the speaker in AirPlay on your iPhone.');
  }
}
function permissionLabel(p) {
  return ({ 'led.ring': w('управление кольцом подсветки', 'control the LED ring'), 'log.write': w('запись в журнал', 'write to the log'), 'system.full-access': w('полный доступ root: микрофон, звук, датчики, файлы, сеть', 'full root access: microphone, audio, sensors, files, network') })[p] || p;
}
async function togglePlugin(p) {
  const native = p.manifest.engine === 'native', enabling = !p.enabled;
  if (enabling && native && !await ask({ title: w('Включить нативный плагин?', 'Enable a native plugin?'), body: w('Он работает с правами root без песочницы и получает полный доступ к колонке.', 'It runs as root without a sandbox and gets full access to the speaker.'), confirm: w('Включить', 'Enable') })) { updateDiagnostics(); return; }
  await api(`/api/plugins/${p.manifest.id}/enable`, { enabled: enabling, acknowledge_native: enabling && native });
  await load();
  toast(enabling ? w('Плагин включён', 'Plugin enabled') : w('Плагин выключен', 'Plugin disabled'), 'ok');
}

/* ── yam: installed package detail ───────────────────────────── */
function yamRuntime(reg) { return yam?.registries?.find(r => r.id === reg)?.runtime || {}; }
function jobFor(reg, name) { return yam?.jobs?.find(j => j.registry === reg && (j.name === name || (!name && j.action === 'setup'))); }
function jobBlock(job) {
  if (!job) return null;
  const tone = job.state === 'running' ? 'busy' : job.state === 'error' ? 'danger' : 'ok';
  const word = job.state === 'running' ? jobLabel(job) : job.state === 'error' ? w('Ошибка: ', 'Failed: ') + (job.action === 'setup' ? 'yam v2' : job.name) : w('Готово: ', 'Done: ') + (job.action === 'setup' ? 'yam v2' : job.name);
  return h('section', { class: 'block', 'data-job': job.id }, h('div', { class: 'block-head' }, h('h3', { class: 't-h3' }, w('Последняя операция', 'Last operation')), status(word, tone)), job.output ? h('pre', { class: 'log' }, job.output) : null);
}
function yamInstalledDetail(item, enter) {
  const reg = item.kind, e = item.entry, out = detailWrap(enter, backButton('plugins'));
  const actions = h('div', { class: 'detail-actions' });
  if (e && e.version && e.version !== item.version && yamRuntime(reg).available) actions.append(button(w('Обновить до ', 'Update to ') + e.version, () => yamJob('install', reg, item.name), 'primary'));
  actions.append(button(w('Удалить', 'Remove'), () => yamRemove(reg, item.name), 'danger', { disabled: !yamRuntime(reg).available || yam?.busy }));
  out.append(h('header', { class: 'detail-head' }, h('h2', { class: 't-title' }, item.name), h('div', { class: 'byline' }, sourceTag(reg), h('span', { class: 't-mono' }, item.version), status(w('Установлен', 'Installed'), 'ok')), actions));
  if (e?.summary) out.append(h('p', { class: 'lede' }, e.summary));
  const facts = h('dl', { class: 'facts' }, h('dt', {}, w('Реестр', 'Registry')), h('dd', { class: 't-mono' }, reg === 'v2' ? 'repo.yse.wtf/v2' : 'nyakokitsu/yapkg'));
  if (e?.homepage) facts.append(h('dt', {}, w('Страница', 'Homepage')), h('dd', {}, h('a', { href: e.homepage, target: '_blank', rel: 'noopener', class: 'ui-link ui-focus' }, e.homepage)));
  out.append(h('section', { class: 'block' }, facts));
  out.append(reg === 'v2'
    ? callout('info', w('Запуск — на стороне хоста', 'The host starts it'), w('yam v2 устанавливает файлы и записывает объявленные сервисы, но сам их не запускает. Сервисы стартуют хост-пакеты, например yam/luna.', 'yam v2 installs files and records declared services but does not run them. Host packages such as yam/luna start them.'))
    : callout('info', w('Пакетом управляет yamd', 'yamd manages this package'), w('Включение и запуск пакетов yam v1 — в веб-интерфейсе yamd на порту 8099.', 'Enable and run yam v1 packages in the yamd web UI on port 8099.'), h('a', { class: 'btn btn--secondary btn--sm ui-press ui-press--on-dark ui-focus', href: `http://${location.hostname}:8099`, target: '_blank', rel: 'noopener' }, h('span', {}, 'yamd :8099'))));
  const job = jobFor(reg, item.name); if (job) out.append(jobBlock(job));
  return out;
}
async function yamJob(action, registry, name) {
  const job = await api('/api/yam/jobs', { action, registry, name });
  yam.busy = true; yam.jobs = [job, ...(yam.jobs || [])]; lastYamBusy = true;
  render(); schedulePoll(800);
}
async function yamRemove(reg, name) {
  if (!await ask({ title: w(`Удалить ${name}?`, `Remove ${name}?`), body: reg === 'v2' ? w('yam удалит файлы пакета. Если от него зависят другие пакеты, yam откажет и объяснит почему.', 'yam removes the package files. If other packages depend on it, yam refuses and explains why.') : w('yamd удалит пакет и его данные.', 'yamd removes the package and its data.'), confirm: w('Удалить', 'Remove'), tone: 'danger' })) return;
  await yamJob('remove', reg, name);
}

/* ── store ───────────────────────────────────────────────────── */
function storeEntries() {
  const out = [];
  for (const e of state?.catalog?.entries || []) {
    const installed = state.plugins?.find(p => p.manifest.id === e.manifest.id);
    out.push({ key: `hb:${e.source_id}:${e.sha256}`, kind: 'homebrew', title: text(e.manifest.title), summary: text(e.manifest.description), version: e.manifest.version, entry: e, installed, compatible: !compat(e.manifest).block });
  }
  for (const r of yam?.registries || []) for (const p of r.packages) out.push({ key: `${r.id}:${p.name}`, kind: r.id, title: p.name, summary: p.summary, version: p.version, entry: p, installed: p.installed, compatible: p.compatible });
  return out;
}
function storePage(enter) {
  const all = storeEntries(), d = device(), q = store.q.trim().toLowerCase();
  const counts = { all: 0, homebrew: 0, v2: 0, v1: 0 };
  const visible = all.filter(e => {
    if (store.mine && d.known && !e.compatible) return false;
    if (q && !(e.title.toLowerCase().includes(q) || e.summary.toLowerCase().includes(q))) return false;
    counts.all++; counts[e.kind]++;
    return store.source === 'all' || store.source === e.kind;
  });
  const wide = matchMedia('(min-width: 961px)').matches;
  let selected = all.find(e => e.key === route.id);
  if (!selected && wide) selected = visible[0];
  const refresh = button('', async () => { await api('/api/catalog/refresh', {}); await load(); toast(w('Каталоги обновлены', 'Catalogs refreshed'), 'ok'); }, 'ghost', { small: true, icon: 'refresh', aria: w('Обновить каталоги', 'Refresh catalogs') });
  const search = h('input', { type: 'search', class: 'input ui-field', placeholder: w('Поиск по названию и описанию', 'Search names and descriptions'), value: store.q, 'aria-label': w('Поиск', 'Search') });
  search.addEventListener('input', () => { store.q = search.value; rerenderStoreList(); });
  const chips = h('div', { class: 'chips', role: 'group', 'aria-label': w('Источник', 'Source') }, [['all', w('Все', 'All')], ['homebrew', w('Плагины', 'Plugins')], ['v2', 'yam v2'], ['v1', 'yam v1']].map(([id, label]) =>
    h('button', { type: 'button', class: 'chip ui-press ui-press--on-dark ui-focus', 'aria-pressed': String(store.source === id), onclick: () => { store.source = id; rerenderStoreList(); } }, h('span', {}, label), h('span', { class: 'count' }, String(counts[id])))));
  const tools = h('div', { class: 'pane-tools' }, h('div', { class: 'search' }, icon('search'), search), chips);
  if (d.known) {
    const sw = switchControl(store.mine, w('Только для моей станции', 'Only for my station'), s => { store.mine = !store.mine; s.setAttribute('aria-checked', String(store.mine)); rerenderStoreList(); });
    tools.append(h('div', { class: 'switch-row' }, sw, h('label', { class: 't-small muted', onclick: () => sw.click() }, w('Только для ', 'Only for ') + pair(d.name))));
  }
  const list = h('div', { class: 'list-pane' }, h('div', { class: 'pane-head' }, h('h1', { class: 't-h2' }, w('Магазин', 'Store')), refresh), tools);
  const failing = [...(state?.sources || []).filter(s => s.enabled && s.error).map(s => s.name), ...(yam?.registries || []).filter(r => r.error).map(r => 'yam ' + r.id)];
  const progress = setupProgress(); if (progress) list.append(progress);
  if (failing.length) list.append(h('div', { class: 'pane-tools' }, callout('warn', w('Недоступно: ', 'Unavailable: ') + failing.join(', '), w('Подробности — на странице «Станция».', 'Details are on the Station page.'))));
  if (!state) list.append(skeletonList());
  else if (!visible.length) list.append(h('div', { class: 'empty' }, h('p', { class: 't-h3' }, w('Ничего не найдено', 'Nothing found')), h('p', {}, q ? w('Измените запрос или выберите другой источник.', 'Change the query or pick another source.') : store.mine && d.known ? w('Для этой станции подходящих пакетов нет. Выключите фильтр, чтобы увидеть все.', 'No packages fit this station. Turn the filter off to see all.') : w('Каталоги ещё загружаются или недоступны.', 'Catalogs are loading or unavailable.')), store.mine && d.known ? button(w('Показать все', 'Show all'), () => { store.mine = false; render(); }, 'secondary') : null));
  else {
    const groups = store.source === 'all' ? [['homebrew', w('Плагины', 'Plugins')], ['v2', w('Пакеты yam v2', 'yam v2 packages')], ['v1', w('Пакеты yam v1, устаревший формат', 'yam v1 packages, legacy format')]] : [[store.source, '']];
    for (const [kind, label] of groups) {
      const group = visible.filter(e => e.kind === kind); if (!group.length) continue;
      if (label) list.append(h('div', { class: 'group-label' }, h('span', {}, label)));
      list.append(h('ul', { class: 'list' }, group.map(e => storeRow(e, selected))));
    }
  }
  const detail = !state ? null : selected ? storeDetail(selected, enter) : h('div', { class: 'empty' }, h('p', { class: 't-h3' }, w('Выберите пакет слева', 'Pick a package on the left')));
  return split(list, detail, !!route.id);
}
function rerenderStoreList() {
  const focusedSearch = document.activeElement?.type === 'search', pos = document.activeElement?.selectionStart;
  render();
  if (focusedSearch) { const s = document.querySelector('.search input'); s.focus(); if (pos != null) s.setSelectionRange(pos, pos); }
}
function storeRow(e, selected) {
  let side;
  if (e.kind === 'homebrew') {
    const current = e.installed, isThis = current?.sha256 === e.entry.sha256, upd = current?.update?.sha256 === e.entry.sha256;
    side = [isThis ? status(w('Установлен', 'Installed'), 'ok') : upd ? tag(w('обновление', 'update'), 'signal') : !e.compatible ? status(w('Не подходит', 'Not for you')) : null];
  } else side = [e.installed ? status(w('Установлен', 'Installed'), 'ok') : !e.compatible ? status(w('Нет сборки', 'No build')) : null];
  return listRow({ key: e.key, page: 'store', title: e.title, sub: e.summary, side, dim: !e.compatible, current: selected?.key === e.key });
}
function storeDetail(e, enter) { return e.kind === 'homebrew' ? homebrewStoreDetail(e, enter) : yamStoreDetail(e, enter); }
function homebrewStoreDetail(e, enter) {
  const entry = e.entry, m = entry.manifest, c = compat(m), current = e.installed;
  const isThis = current?.sha256 === entry.sha256, canUpdate = current?.update?.sha256 === entry.sha256;
  const actions = h('div', { class: 'detail-actions' });
  if (isThis) actions.append(status(w('Эта версия установлена', 'This version is installed'), 'ok'), button(w('Открыть', 'Open'), () => go('plugins', 'hb:' + m.id), 'secondary'));
  else if (canUpdate) actions.append(button(w('Обновить', 'Update'), () => updatePlugin(current, entry), 'primary'));
  else actions.append(button(w('Установить', 'Install'), async () => showConfirmation(await api('/api/catalog/inspect', { sha256: entry.sha256, source_id: entry.source_id })), 'primary', { disabled: !!c.block }));
  const out = detailWrap(enter, backButton('store'), h('header', { class: 'detail-head' }, h('h2', { class: 't-title' }, e.title), h('div', { class: 'byline' }, sourceTag('homebrew', entry), h('span', { class: 'version' }, 'v' + m.version), entry.source_official ? null : h('span', {}, m.author)), actions));
  if (e.summary) out.append(h('p', { class: 'lede' }, e.summary));
  if (c.block) out.append(callout('danger', w('Не подходит этой станции', 'Does not fit this station'), c.block));
  else if (c.note) out.append(callout('warn', w('Ограничение этой станции', 'Limitation on this station'), c.note));
  if (m.engine === 'native') out.append(callout('warn', w('Нативный плагин', 'Native plugin'), w('Работает с правами root без песочницы. Ставьте только код, которому доверяете.', 'Runs as root without a sandbox. Install only code you trust.')));
  const d = device();
  out.append(h('section', { class: 'block' }, h('dl', { class: 'facts' },
    h('dt', {}, w('Источник', 'Source')), h('dd', {}, entry.source_name || '—'),
    h('dt', {}, w('Подпись', 'Signature')), h('dd', {}, status(w('каталог подписан, проверено', 'signed catalog, verified'), 'ok')),
    h('dt', {}, w('Разрешения', 'Permissions')), h('dd', {}, m.permissions.map(permissionLabel).join('; ') || '—'),
    h('dt', {}, w('Станции', 'Stations')), h('dd', {}, m.devices?.length ? m.devices.map(id => tag(id, id === d.id ? 'mine' : '')) : w('любая с root', 'any rooted station')),
    h('dt', {}, w('Размер', 'Size')), h('dd', {}, formatBytes(entry.bytes)),
    h('dt', {}, w('Одобрено', 'Approved')), h('dd', {}, formatDate(entry.reviewed_at)),
    h('dt', {}, 'SHA-256'), h('dd', { class: 'hash' }, entry.sha256))));
  return out;
}
function yamStoreDetail(e, enter) {
  const p = e.entry, reg = e.kind, rt = yamRuntime(reg), d = device(), job = jobFor(reg, p.name);
  const actions = h('div', { class: 'detail-actions' });
  const running = yam?.busy;
  if (p.installed) {
    actions.append(status(w('Установлен ', 'Installed ') + p.installed, 'ok'));
    if (p.installed !== p.version && rt.available && p.compatible) actions.append(button(w('Обновить', 'Update'), () => yamInstall(e), 'primary', { disabled: running }));
    actions.append(button(w('Удалить', 'Remove'), () => yamRemove(reg, p.name), 'danger', { disabled: !rt.available || running }));
  } else actions.append(button(w('Установить', 'Install'), () => yamInstall(e), 'primary', { disabled: !rt.available || !p.compatible || running }));
  const out = detailWrap(enter, backButton('store'), h('header', { class: 'detail-head' }, h('h2', { class: 't-title' }, p.name), h('div', { class: 'byline' }, sourceTag(reg), h('span', { class: 't-mono' }, p.version), reg === 'v1' ? tag(w('устаревший', 'legacy')) : null), actions));
  if (p.summary) out.append(h('p', { class: 'lede' }, p.summary));
  if (!p.compatible) out.append(callout('danger', w('Нет сборки для этой станции', 'No build for this station'), p.reason === 'arch' ? w(`Пакет собран под другую архитектуру, а у станции ${d.arch}.`, `Built for another architecture; this station is ${d.arch}.`) : w(`Пакет собран для: ${(p.targets || []).join(', ')}.`, `Built for: ${(p.targets || []).join(', ')}.`)));
  else if (!rt.available) out.append(runtimeCallout(reg, rt));
  out.append(h('section', { class: 'block' }, h('dl', { class: 'facts' },
    h('dt', {}, w('Реестр', 'Registry')), h('dd', { class: 't-mono' }, reg === 'v2' ? 'repo.yse.wtf/v2' : 'nyakokitsu/yapkg'),
    h('dt', {}, w('Подпись', 'Signature')), h('dd', {}, reg === 'v2' ? status(w('индекс подписан Ed25519, ключ закреплён', 'Ed25519-signed index, pinned key'), 'ok') : status(w('без подписи, только SHA-256 архива', 'unsigned, archive SHA-256 only'))),
    h('dt', {}, w('Станции', 'Stations')), h('dd', {}, p.targets?.length ? p.targets.map(id => tag(id, id === d.id ? 'mine' : '')) : w('любая', 'any')),
    p.features?.length ? [h('dt', {}, w('Требует', 'Requires')), h('dd', {}, p.features.map(f => tag(f)))] : null,
    reg === 'v2' ? [h('dt', {}, w('Звёзды в реестре', 'Registry stars')), h('dd', { class: 't-mono' }, String(p.stars || 0))] : null,
    p.homepage ? [h('dt', {}, w('Страница', 'Homepage')), h('dd', {}, h('a', { href: p.homepage, target: '_blank', rel: 'noopener', class: 'ui-link ui-focus' }, p.homepage))] : null)));
  if (job) out.append(jobBlock(job));
  return out;
}
function runtimeCallout(reg, rt) {
  if (rt.reason === 'preview') return callout('info', w('Предпросмотр на компьютере', 'Computer preview'), w('Пакеты yam ставятся на самой станции. Здесь установка недоступна.', 'yam packages install on the station itself. Installing is unavailable here.'));
  if (reg === 'v1') return callout('warn', w('yamd (yam v1) не запущен', 'yamd (yam v1) is not running'), w('Пакеты v1 ставит демон yamd на порту 8099. Автор yam прекратил поддержку v1; новые пакеты выходят в yam v2.', 'yamd on port 8099 installs v1 packages. The yam author ended v1 support; new packages ship in yam v2.'),
    rt.can_setup ? button(w('Установить yamd', 'Install yamd'), () => yamJob('setup', 'v1', ''), 'primary', { disabled: yam?.busy }) : null);
  if (rt.reason === 'unknown-device') return callout('warn', w('Модель станции не определена', 'Station model is unknown'), w('Запустите менеджер с флагом --platform, например --platform yandexmini.', 'Start the manager with --platform, for example --platform yandexmini.'));
  const job = jobFor('v2', '');
  return callout('warn', w('yam v2 не установлен на станции', 'yam v2 is not on the station'), w('Менеджер скачает последний стабильный yam с GitHub, сверит SHA-256 и положит его в /data/yam. Системные файлы не меняются.', 'The manager downloads the latest stable yam from GitHub, checks SHA-256 and puts it in /data/yam. System files are untouched.'),
    button(w('Установить yam v2', 'Install yam v2'), () => yamJob('setup', 'v2', ''), 'primary', { disabled: !rt.can_setup || yam?.busy }), job?.state === 'running' ? status(jobLabel(job), 'busy') : null);
}
async function yamInstall(e) {
  const p = e.entry, reg = e.kind;
  const body = reg === 'v2'
    ? w('yam скачает пакет и зависимости из repo.yse.wtf, проверит подпись индекса и SHA-256 архивов. Нужные пакеты Entware будут поставлены без отдельного вопроса.', 'yam downloads the package and its dependencies from repo.yse.wtf and checks the index signature and archive SHA-256. Required Entware packages are installed without a separate prompt.')
    : w('yamd скачает .yap из реестра yapkg и сверит SHA-256. Индекс v1 не подписан: ставьте пакеты авторов, которым доверяете.', 'yamd downloads the .yap from the yapkg registry and checks SHA-256. The v1 index is unsigned: install packages from authors you trust.');
  if (!await ask({ title: w(`Установить ${p.name} ${p.version}?`, `Install ${p.name} ${p.version}?`), body, confirm: w('Установить', 'Install') })) return;
  await yamJob('install', reg, p.name);
}

/* ── install confirmation for Homebrew packages ──────────────── */
function showConfirmation(staged) {
  const m = staged.manifest, plan = staged.storage, checks = [];
  const blocking = staged.incompatible;
  const install = button(staged.update ? w('Обновить', 'Update') : w('Установить', 'Install'), async () => {
    try {
      if (staged.update) { await finishPluginUpdate(staged, true); return; }
      await api('/api/install', { ticket: staged.ticket, permissions: m.permissions, acknowledge_local: !!local?.checked });
      closeDialog(); await load(); go('plugins', 'hb:' + m.id); toast(w('Плагин установлен. Включите его переключателем.', 'Plugin installed. Turn it on with the switch.'), 'ok');
    } catch (error) { if (error.code === 'insufficient_storage') { storageDialog(error.details, staged); return; } throw error; }
  }, 'primary');
  const sync = () => { install.disabled = !!blocking || checks.some(c => !c.checked) || plan?.fits === false; };
  for (const perm of m.permissions) { const c = checkbox(permissionLabel(perm), sync); checks.push(c); }
  let local = null;
  if (!staged.reviewed) { local = checkbox(w('Понимаю: пакет не проходил модерацию, автору доверяю', 'I understand this package is unreviewed and trust its author'), sync); checks.push(local); }
  const parts = [h('h2', { id: 'dialog-title', class: 't-h2' }, (staged.update ? w('Обновить ', 'Update ') : w('Установить ', 'Install ')) + text(m.title) + '?'),
    h('div', { class: 'byline button-row' }, staged.reviewed ? tag(w('из каталога', 'from catalog'), 'signal') : tag(w('локальный', 'local')), h('span', { class: 't-mono' }, 'v' + m.version), h('span', { class: 'muted' }, m.author))];
  if (text(m.description)) parts.push(h('p', { class: 'muted' }, text(m.description)));
  if (blocking) parts.push(callout('danger', w('Не подходит этой станции', 'Does not fit this station'), blocking));
  else if (staged.compatibility_note) parts.push(callout('warn', w('Ограничение этой станции', 'Limitation on this station'), compat(m).note || staged.compatibility_note));
  if (m.engine === 'native') parts.push(callout('warn', w('Нативный плагин', 'Native plugin'), w('Получит полный доступ к колонке. Ставьте только код, которому доверяете.', 'It gets full access to the speaker. Install only code you trust.')));
  if (!staged.reviewed) parts.push(callout('warn', w('Пакет не из каталога', 'Not from a catalog'), w('Этот файл не проходил модерацию источника.', 'This file was not reviewed by a source.')));
  if (checks.length) parts.push(h('div', { class: 'field' }, h('span', { class: 'field-label' }, w('Разрешения', 'Permissions')), checks.map(c => c.row)));
  if (plan) parts.push(h('p', { class: 't-small ' + (plan.fits ? 'muted' : '') }, `${plan.fits ? w('Места достаточно', 'Enough storage') : w('Недостаточно места', 'Not enough storage')} · ${w('пакет', 'package')} ${formatBytes(staged.bytes || plan.package_bytes)} · ${w('доступно', 'available')} ${formatBytes(plan.available_bytes)}`));
  parts.push(h('p', { class: 'hash' }, 'SHA-256 ' + staged.sha256));
  const foot = h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'));
  if (plan && !plan.fits) foot.append(button(w('Освободить место', 'Free up storage'), () => storageDialog(plan, staged), 'secondary'));
  foot.append(install); parts.push(foot);
  sync(); openDialog(...parts);
}
function storageDialog(plan, staged) {
  const choices = [];
  const sync = () => { const sel = choices.filter(c => c.box.checked), bytes = sel.reduce((n, c) => n + c.plugin.bytes, 0); remove.disabled = !sel.length; note.textContent = `${w('Выбрано', 'Selected')}: ${formatBytes(bytes)}${bytes < plan.missing_bytes ? ' · ' + w('этого может не хватить', 'this may not be enough') : ''}`; };
  for (const p of plan.candidates || []) choices.push({ plugin: p, box: checkbox(`${text(p.title)} · ${formatBytes(p.bytes)}`, sync) });
  const note = h('p', { class: 't-small muted' });
  const retry = async () => { const fresh = await api('/api/storage/plan', { ticket: staged.ticket }); if (fresh.fits) { const next = { ...staged, storage: fresh }; if (next.update && next.fast_update) await finishPluginUpdate(next); else showConfirmation(next); } else storageDialog(fresh, staged); };
  const remove = button(w('Удалить выбранные и продолжить', 'Remove selected and continue'), async () => { for (const c of choices.filter(c => c.box.checked)) await api(`/api/plugins/${c.plugin.id}/remove`, {}); await load(); await retry(); }, 'primary');
  openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, w('Недостаточно места', 'Not enough storage')),
    h('p', { class: 'muted' }, `${w('Нужно освободить', 'Free at least')} ${formatBytes(plan.missing_bytes)}. ${w('8 МиБ всегда остаются системе.', '8 MiB always stay free for the system.')}`),
    choices.length ? h('div', { class: 'field' }, h('span', { class: 'field-label' }, w('Удалить плагины вместе с настройками (сначала крупные)', 'Remove plugins with their settings (largest first)')), choices.map(c => c.box.row)) : callout('warn', w('Удалять нечего', 'Nothing to remove'), w('Освободите место на колонке вручную и проверьте снова.', 'Free storage on the speaker manually and check again.')),
    note, h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), button(w('Проверить снова', 'Check again'), retry, 'secondary'), remove));
  sync();
}
async function updatePlugin(p, entry = p.update) {
  if (!entry) return;
  const staged = await api('/api/catalog/inspect', { sha256: entry.sha256, source_id: entry.source_id });
  staged.update = { id: p.manifest.id, expected_sha256: p.sha256 };
  const expanded = staged.manifest.permissions.some(x => !p.manifest.permissions.includes(x)) || (staged.manifest.engine === 'native' && (p.manifest.engine !== 'native' || !p.native_consent));
  staged.fast_update = !expanded;
  if (staged.storage?.fits === false) { storageDialog(staged.storage, staged); return; }
  if (expanded) { showConfirmation(staged); return; }
  await finishPluginUpdate(staged);
}
async function finishPluginUpdate(staged, acknowledge = false) {
  try { await api(`/api/plugins/${staged.update.id}/update`, { ticket: staged.ticket, expected_sha256: staged.update.expected_sha256, permissions: acknowledge ? staged.manifest.permissions : [], acknowledge_permissions: acknowledge }); }
  catch (error) { if (error.code === 'insufficient_storage') { storageDialog(error.details, staged); return; } throw error; }
  closeDialog(); await load(); go('plugins', 'hb:' + staged.update.id); toast(w('Плагин обновлён, настройки сохранены', 'Plugin updated, settings kept'), 'ok');
}

/* ── add package: drop zone, file picker, URL ────────────────── */
const MAX_PACKAGE = 16 * 1024 * 1024, MAX_YAM = 64 * 1024 * 1024;
const PACKAGE_TYPES = '.aliceplugin,.yampkg,.yap';
const packageKind = name => /\.aliceplugin$/i.test(name) ? 'homebrew' : /\.yampkg$/i.test(name) ? 'v2' : /\.yap$/i.test(name) ? 'v1' : '';
function packageProblem(file) {
  const kind = packageKind(file.name);
  if (!kind) return w(`«${file.name}» — не пакет: нужен .aliceplugin, .yampkg или .yap`, `“${file.name}” is not a package: use .aliceplugin, .yampkg or .yap`);
  if (kind === 'homebrew' && file.size > MAX_PACKAGE) return w('Плагин больше 16 МиБ', 'The plugin is larger than 16 MiB');
  if (kind !== 'homebrew' && file.size > MAX_YAM) return w('Пакет yam больше 64 МиБ', 'The yam package is larger than 64 MiB');
  if (kind !== 'homebrew' && !yamRuntime(kind).available) return kind === 'v2' ? w('Для .yampkg нужен yam v2 на станции — установите его в магазине', '.yampkg needs yam v2 on the station; install it from the store') : w('Для .yap нужен запущенный yamd (yam v1)', '.yap needs yamd (yam v1) running');
  return '';
}
async function inspectFile(file) {
  const problem = packageProblem(file); if (problem) throw Error(problem);
  const kind = packageKind(file.name);
  if (kind === 'homebrew') { showConfirmation(await api('/api/inspect', file, true)); return; }
  yamLocalConfirm(file, kind);
}
/* A local yam archive has no registry signature: the user confirms trust. */
function yamLocalConfirm(file, kind) {
  let trust;
  const install = button(w('Установить', 'Install'), async () => {
    const job = await api('/api/yam/local?file=' + encodeURIComponent(file.name), file, true);
    closeDialog(); yam.busy = true; yam.jobs = [job, ...(yam.jobs || [])]; lastYamBusy = true;
    toast(w('yam устанавливает ', 'yam is installing ') + file.name); schedulePoll(800);
  }, 'primary');
  trust = checkbox(w('Понимаю: пакет не из реестра, автору доверяю', 'I understand this package is not from a registry and trust its author'), () => install.disabled = !trust.checked);
  install.disabled = true;
  openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, w('Установить ', 'Install ') + file.name + '?'),
    h('p', { class: 'muted' }, `${kind === 'v2' ? 'yam v2' : w('yam v1 (yamd)', 'yam v1 (yamd)')} · ${formatBytes(file.size)}`),
    callout('warn', w('Пакет не из реестра', 'Not from a registry'), kind === 'v2' ? w('Подпись не проверяется. yam проверит только архив, платформу и конфликты; пакеты Entware, если нужны, поставит без отдельного вопроса.', 'No signature is checked. yam only checks the archive, platform and conflicts; required Entware packages are installed without a separate prompt.') : w('Подпись не проверяется: yamd установит архив как есть.', 'No signature is checked: yamd installs the archive as is.')),
    trust.row, h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), install));
}
function addDialog(mode = 'file') {
  const fileTab = h('button', { type: 'button', class: 'chip ui-press ui-press--on-dark ui-focus', 'aria-pressed': String(mode === 'file'), onclick: () => addDialog('file') }, w('Файл', 'File'));
  const urlTab = h('button', { type: 'button', class: 'chip ui-press ui-press--on-dark ui-focus', 'aria-pressed': String(mode === 'url'), onclick: () => addDialog('url') }, w('По ссылке', 'From URL'));
  const head = [h('h2', { id: 'dialog-title', class: 't-h2' }, w('Добавить пакет', 'Add a package')), h('div', { class: 'tabs-inline', role: 'group' }, fileTab, urlTab)];
  if (mode === 'file') {
    const input = h('input', { type: 'file', accept: PACKAGE_TYPES, class: 'sr', tabindex: '-1', 'aria-hidden': 'true' });
    const msg = h('span', { class: 't-small', 'aria-live': 'polite' }, w('.aliceplugin до 16 МиБ · .yampkg и .yap до 64 МиБ', '.aliceplugin up to 16 MiB · .yampkg and .yap up to 64 MiB'));
    const choose = button(w('Выбрать файл', 'Choose file'), () => input.click(), 'secondary');
    const zone = h('div', { class: 'dropzone' }, h('span', { class: 'drop-glyph', 'aria-hidden': 'true' }), h('strong', {}, w('Перетащите пакет сюда', 'Drop a package here')), choose, msg);
    const take = file => { const problem = packageProblem(file); if (problem) { zone.dataset.state = 'reject'; msg.textContent = problem; BetterUI.shake(zone); return; } zone.dataset.state = 'busy'; msg.textContent = w('Проверяем пакет…', 'Checking the package…'); inspectFile(file).catch(e => { zone.dataset.state = 'reject'; msg.textContent = errorText(e); }); };
    input.onchange = () => input.files[0] && take(input.files[0]);
    let depth = 0;
    zone.addEventListener('dragenter', e => { e.preventDefault(); depth++; zone.dataset.state = 'over'; msg.textContent = w('Отпустите, чтобы проверить', 'Release to check'); });
    zone.addEventListener('dragleave', () => { if (--depth <= 0) { depth = 0; delete zone.dataset.state; msg.textContent = w('.aliceplugin до 16 МиБ · .yampkg и .yap до 64 МиБ', '.aliceplugin up to 16 MiB · .yampkg and .yap up to 64 MiB'); } });
    zone.addEventListener('dragover', e => e.preventDefault());
    zone.addEventListener('drop', e => { e.preventDefault(); e.stopPropagation(); depth = 0; const f = e.dataTransfer.files; if (f.length > 1) { zone.dataset.state = 'reject'; msg.textContent = w('Перетащите один пакет', 'Drop one package'); return; } if (f[0]) take(f[0]); });
    openDialog(...head, zone, input, h('p', { class: 't-small muted' }, w('Пакет не из каталога: перед установкой вы подтвердите доверие автору. .yampkg ставит yam v2, .yap — yamd.', 'Not from a catalog: you confirm trust in the author first. yam v2 installs .yampkg, yamd installs .yap.')), h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost')));
  } else {
    const url = h('input', { type: 'url', id: 'add-url', class: 'input ui-field', required: true, placeholder: 'http://192.168.1.20:8000/plugin.aliceplugin' });
    const hash = h('input', { id: 'add-hash', class: 'input ui-field t-mono', pattern: '[0-9a-f]{64}', placeholder: 'SHA-256' });
    const submit = button(w('Скачать на колонку', 'Download to speaker'), null, 'primary', { submit: true });
    const form = h('form', { class: 'dialog-inner', novalidate: true, style: { padding: '0' } },
      h('div', { class: 'field' }, h('label', { for: 'add-url' }, w('Ссылка на .aliceplugin', '.aliceplugin URL')), url, h('span', { class: 't-small faint' }, w('Скачивает сама колонка: для файла на ПК укажите IP компьютера, не localhost.', 'The speaker downloads it: for a file on your PC use the computer’s IP, not localhost.'))),
      h('div', { class: 'field' }, h('label', { for: 'add-hash' }, w('Ожидаемый SHA-256 (необязательно)', 'Expected SHA-256 (optional)')), hash),
      h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), submit));
    form.addEventListener('submit', e => { e.preventDefault(); const bad = form.querySelector(':invalid'); if (bad) { BetterUI.shake(bad); bad.focus(); toast(bad === url ? w('Укажите ссылку http(s)', 'Enter an http(s) URL') : w('SHA-256 — 64 символа 0-9 a-f', 'SHA-256 is 64 hex characters'), 'danger'); return; } perform(submit, async () => showConfirmation(await api('/api/fetch', { url: url.value.trim(), sha256: hash.value.trim() }))); });
    openDialog(...head, form);
  }
}
/* Window-level drop: anywhere outside a dialog accepts one package. */
(() => {
  let depth = 0;
  const hasFiles = e => [...(e.dataTransfer?.types || [])].includes('Files');
  window.addEventListener('dragenter', e => { if (!hasFiles(e) || $('dialog').open) return; depth++; document.body.dataset.drag = ''; });
  window.addEventListener('dragleave', e => { if (!hasFiles(e)) return; if (--depth <= 0) { depth = 0; delete document.body.dataset.drag; } });
  window.addEventListener('dragover', e => { if (hasFiles(e)) e.preventDefault(); });
  window.addEventListener('drop', e => {
    if (!hasFiles(e)) return; e.preventDefault(); depth = 0; delete document.body.dataset.drag;
    if ($('dialog').open) return;
    const files = e.dataTransfer.files; if (files.length !== 1) { toast(w('Перетащите один пакет', 'Drop one package'), 'danger'); return; }
    perform(null, () => inspectFile(files[0]));
  });
})();

/* ── station page ────────────────────────────────────────────── */
function stationPage(enter) {
  const s = state?.system || {}, d = device(), disk = s.disk || {}, mem = s.memory || {};
  const page = h('div', { class: 'station', data: { enter } }, h('h1', { class: 't-h2' }, w('Станция', 'Station')));
  if (!state) { page.append(skeletonList()); return page; }
  const grid = h('div', { class: 'station-grid' });
  const detected = { 'device-tree': w('по device tree', 'from device tree'), flag: w('флаг --platform', '--platform flag'), preview: w('компьютер', 'computer') }[d.detected] || d.detected;
  grid.append(h('section', { class: 'block' }, ringElement(true),
    h('h2', { class: 't-h2' }, pair(d.name)),
    h('div', {}, kv(w('Профиль', 'Profile'), d.id, true), kv(w('Определено', 'Detected'), detected), d.model ? kv(w('Модель', 'Model'), d.model, true) : null, kv(w('Архитектура', 'Architecture'), `${s.os || ''} / ${d.arch || s.architecture || ''}`, true),
      kv(w('Кольцо подсветки', 'LED ring'), s.hardware_led ? w('выводится на светодиоды', 'driving the LEDs') : d.led_ring ? w('драйвер есть, не включён', 'driver present, off') : w('нет драйвера для этой модели', 'no driver for this model')),
      kv(w('Плагины', 'Plugins'), `${s.plugin_count || 0} / ${s.max_plugins || 8} · ${w('работают', 'running')} ${s.running_count || 0} / ${s.max_running || 4}`))));
  grid.append(h('section', { class: 'block' }, h('h3', { class: 't-h3' }, w('Память', 'Storage and memory')),
    h('div', { class: 'stat' }, h('span', { class: 'faint t-small' }, w('Свободно для плагинов', 'Free for plugins')), h('span', { class: 'stat-value' }, disk.available ? formatBytes(disk.plugin_available_bytes) : '—', h('small', {}, disk.available ? ' / ' + formatBytes(disk.total_bytes) : '')), disk.available ? meter(disk.used_bytes, disk.total_bytes) : null),
    h('div', { class: 'stat' }, h('span', { class: 'faint t-small' }, w('Свободно ОЗУ', 'Free memory')), h('span', { class: 'stat-value' }, mem.available ? formatBytes(mem.available_bytes) : '—', h('small', {}, mem.available ? ' / ' + formatBytes(mem.total_bytes) : '')), mem.available ? meter(mem.used_bytes, mem.total_bytes) : null),
    kv(w('Homebrew в памяти', 'Homebrew memory'), formatBytes(mem.process_bytes || mem.go_heap_bytes)), kv(w('Резерв для системы', 'System reserve'), formatBytes(disk.reserve_bytes || 8 << 20))));
  if (state.updates) grid.append(managerUpdates(state.updates));
  grid.append(storageBlock());
  grid.append(sourcesBlock());
  grid.append(h('section', { class: 'block wide' }, h('h3', { class: 't-h3' }, w('Разработчикам', 'Developers')), h('div', { class: 'button-row' },
    h('a', { class: 'btn btn--secondary btn--sm ui-press ui-press--on-dark ui-focus', href: 'https://github.com/hertebeznat-cell/alice-homebrew/blob/main/docs/PLUGIN_GUIDE.md', target: '_blank', rel: 'noopener' }, h('span', {}, w('Гайд по плагинам', 'Plugin guide')), icon('link')),
    h('a', { class: 'btn btn--secondary btn--sm ui-press ui-press--on-dark ui-focus', href: 'https://github.com/nyakokitsu/yam/tree/main/docs', target: '_blank', rel: 'noopener' }, h('span', {}, w('Документация yam', 'yam docs')), icon('link')),
    button(w('Оборудование (JSON)', 'Hardware (JSON)'), async () => { const hw = await api('/api/hardware'); openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, w('Оборудование станции', 'Station hardware')), h('pre', { class: 'log' }, JSON.stringify(hw, null, 2)), h('div', { class: 'dialog-foot' }, button(w('Закрыть', 'Close'), () => closeDialog(), 'secondary'))); }, 'secondary', { small: true, icon: 'term' }))));
  page.append(grid);
  return page;
}
function kv(label, value, mono) { return h('div', { class: 'kv' }, h('span', {}, label), h('strong', { class: mono ? 't-mono' : null }, value ?? '—')); }
function managerUpdates(u) {
  const busy = ['checking', 'downloading', 'restarting'].includes(u.state);
  const tone = u.state === 'error' || u.state === 'rolled-back' ? 'danger' : busy ? 'busy' : u.state === 'up-to-date' ? 'ok' : '';
  const sw = switchControl(u.enabled, w('Устанавливать обновления автоматически', 'Install updates automatically'), async s => { s.setAttribute('aria-checked', String(!u.enabled)); state.updates = await api('/api/updates/settings', { enabled: !u.enabled }); render(); });
  if (busy || !u.supported) sw.disabled = true;
  return h('section', { class: 'block' }, h('div', { class: 'block-head' }, h('h3', { class: 't-h3' }, w('Обновления менеджера', 'Manager updates')), status(updateWord(u.state) || '—', tone)),
    h('div', { class: 'kv' }, h('span', {}, w('Версия', 'Version')), h('strong', { class: 'version' }, u.current_version + (about.codename ? ' ' + about.codename : ''),
      about.commit ? [' · ', h('a', { class: 'ui-link ui-focus', href: 'https://github.com/hertebeznat-cell/alice-homebrew/commit/' + about.commit, target: '_blank', rel: 'noopener', title: w('Коммит сборки', 'Build commit') }, about.commit)] : null,
      u.latest_version && u.latest_version !== u.current_version ? ' → ' + u.latest_version : null)),
    kv(w('Последняя проверка', 'Last check'), formatDate(u.last_check)), kv(w('Следующая', 'Next'), formatDate(u.next_check)),
    u.error ? callout('danger', w('Ошибка обновления', 'Update error'), u.error) : null,
    !u.supported ? h('p', { class: 't-small faint' }, w('Автоустановка работает только в службе на станции.', 'Automatic install works only in the station service.')) : null,
    h('div', { class: 'switch-row' }, sw, h('label', { class: 't-small', onclick: () => sw.click() }, w('Устанавливать автоматически (раз в 5 часов)', 'Install automatically (every 5 hours)'))),
    h('div', { class: 'button-row' }, button(w('Проверить сейчас', 'Check now'), async () => { await api('/api/updates/check', {}); state.updates = await api('/api/updates'); render(); }, 'secondary', { small: true, disabled: busy }),
      h('a', { class: 'btn btn--ghost btn--sm ui-dim ui-focus', href: 'https://github.com/hertebeznat-cell/alice-homebrew/releases', target: '_blank', rel: 'noopener' }, h('span', {}, w('Релизы', 'Releases')), icon('link'))));
}
function storageBlock() {
  const plugins = [...(state.plugins || [])].sort((a, b) => (b.storage_bytes || 0) - (a.storage_bytes || 0));
  const box = h('section', { class: 'block' }, h('h3', { class: 't-h3' }, w('Место плагинов', 'Plugin storage')));
  if (!plugins.length) box.append(h('p', { class: 'muted' }, w('Плагины Homebrew не установлены.', 'No Homebrew plugins installed.')));
  for (const p of plugins) box.append(h('div', { class: 'kv' }, h('span', {}, text(p.manifest.title)), h('span', { class: 'button-row' }, h('strong', { class: 't-mono' }, formatBytes(p.storage_bytes || 0)), button(w('Открыть', 'Open'), () => go('plugins', 'hb:' + p.manifest.id), 'ghost', { small: true }))));
  return box;
}
function sourcesBlock() {
  const box = h('section', { class: 'block wide' }, h('div', { class: 'block-head' }, h('h3', { class: 't-h3' }, w('Источники', 'Sources')), button(w('Добавить источник', 'Add source'), () => sourceDialog(), 'secondary', { small: true, icon: 'plus' })));
  for (const s of state.sources || []) {
    const count = state.catalog?.entries?.filter(p => p.source_id === s.id).length || 0;
    const row = h('div', { class: 'source' }, h('div', { class: 'source-top' }, h('strong', {}, s.name), h('span', {}, tag(s.official ? w('официальный', 'official') : w('сторонний', 'community'), s.official ? 'signal' : ''), status(s.error ? w('ошибка', 'error') : s.enabled ? w('включён', 'on') : w('выключен', 'off'), s.error ? 'danger' : s.enabled ? 'ok' : ''))),
      h('span', { class: 'hash' }, s.url), h('span', { class: 't-small muted' }, `${w('Плагинов', 'Plugins')}: ${count} · ${w('ревизия', 'revision')} ${s.revision} · ${w('подпись Ed25519', 'Ed25519 signature')}`));
    if (s.error) row.append(callout('danger', w('Источник недоступен', 'Source unavailable'), s.error));
    const actions = h('div', { class: 'button-row' }, button(w('Обновить', 'Refresh'), async () => { try { await api(`/api/sources/${s.id}/refresh`, {}); } finally { await load(); } }, 'secondary', { small: true, disabled: !s.enabled }),
      button(s.enabled ? w('Выключить', 'Disable') : w('Включить', 'Enable'), async () => { await api(`/api/sources/${s.id}/enabled`, { enabled: !s.enabled }); await load(); }, 'ghost', { small: true }),
      button(w('Удалить', 'Remove'), async () => { if (!await ask({ title: w(`Удалить источник ${s.name}?`, `Remove source ${s.name}?`), body: w('Установленные из него плагины останутся.', 'Plugins installed from it stay.'), confirm: w('Удалить', 'Remove'), tone: 'danger' })) return; await api(`/api/sources/${s.id}/remove`, {}); await load(); }, 'danger', { small: true }));
    row.append(actions); box.append(row);
  }
  for (const r of yam?.registries || []) {
    const rt = r.runtime || {};
    const runtimeWord = rt.available ? w('установлен на станции', 'on the station') : rt.reason === 'preview' ? w('предпросмотр', 'preview') : r.id === 'v1' ? w('yamd не запущен', 'yamd not running') : w('не установлен', 'not installed');
    box.append(h('div', { class: 'source' }, h('div', { class: 'source-top' }, h('strong', {}, r.id === 'v2' ? 'yam v2 · repo.yse.wtf' : 'yam v1 · yapkg'), h('span', {}, tag(r.id === 'v2' ? 'yam v2' : w('yam v1 · устаревший', 'yam v1 · legacy'), r.id === 'v2' ? 'info' : ''), status(r.error ? w('ошибка', 'error') : runtimeWord, r.error ? 'danger' : rt.available ? 'ok' : ''))),
      h('span', { class: 'hash' }, r.url),
      h('span', { class: 't-small muted' }, `${w('Пакетов', 'Packages')}: ${r.packages.length} · ${r.signed ? w('подпись Ed25519 проверена, ключ закреплён', 'Ed25519 signature verified, pinned key') : w('индекс без подписи', 'unsigned index')} · ${w('обновлено', 'updated')} ${formatDate(r.updated_at)}`),
      r.error ? callout('danger', w('Реестр недоступен', 'Registry unavailable'), r.error) : null));
  }
  return box;
}
function sourceDialog() {
  const url = h('input', { type: 'url', id: 'src-url', class: 'input ui-field', required: true, placeholder: 'https://github.com/author/plugin-source' });
  const name = h('input', { id: 'src-name', class: 'input ui-field', maxlength: 80 });
  const key = h('input', { id: 'src-key', class: 'input ui-field t-mono', autocomplete: 'off' });
  const submit = button(w('Проверить источник', 'Check source'), null, 'primary', { submit: true });
  const form = h('form', { class: 'dialog-inner', novalidate: true, style: { padding: '0' } },
    h('div', { class: 'field' }, h('label', { for: 'src-url' }, w('Ссылка на источник', 'Source URL')), url, h('span', { class: 't-small faint' }, w('GitHub-репозиторий, сайт источника или catalog/v1.json.', 'A GitHub repository, source site or catalog/v1.json.'))),
    h('div', { class: 'field' }, h('label', { for: 'src-name' }, w('Название (необязательно)', 'Name (optional)')), name),
    h('details', { class: 'field ui-disclosure' }, h('summary', { class: 'field-label ui-focus' }, w('Публичный ключ (обычно определяется сам)', 'Public key (usually detected)')), key),
    h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), submit));
  form.addEventListener('submit', e => { e.preventDefault(); if (!url.checkValidity()) { BetterUI.shake(url); url.focus(); toast(w('Укажите ссылку https', 'Enter an https URL'), 'danger'); return; } perform(submit, async () => sourceConfirmation(await api('/api/sources/inspect', { url: url.value.trim(), name: name.value.trim(), public_key: key.value.trim() }))); });
  openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, w('Добавить источник', 'Add a source')), form);
}
function sourceConfirmation(preview) {
  const s = preview.source;
  let trust = null;
  const add = button(w('Добавить источник', 'Add source'), async () => { await api('/api/sources', { url: s.url, public_key: s.public_key, name: s.name, acknowledge_unofficial: !!trust?.checked }); closeDialog(); await load(); toast(w('Источник добавлен', 'Source added'), 'ok'); }, 'primary');
  if (!s.official) { trust = checkbox(w('Доверяю автору и добавляю сторонний источник', 'I trust the author and add this community source'), () => add.disabled = !trust.checked); add.disabled = true; }
  openDialog(h('h2', { id: 'dialog-title', class: 't-h2' }, s.name), h('p', { class: 'hash' }, s.url),
    callout('ok', w('Подпись каталога проверена', 'Catalog signature verified'), w(`Плагинов в каталоге: ${preview.plugin_count}`, `Plugins in catalog: ${preview.plugin_count}`)),
    !s.official ? callout('warn', w('Сторонний источник', 'Community source'), w('Его плагины проверяет владелец источника, а не Alice Homebrew.', 'Its owner reviews its plugins, not Alice Homebrew.')) : null,
    trust?.row, h('p', { class: 'hash' }, s.public_key), h('div', { class: 'dialog-foot' }, button(w('Отмена', 'Cancel'), () => closeDialog(), 'ghost'), add));
}

/* ── render + live updates ───────────────────────────────────── */
let renderedPage = '', renderedId = '';
function render(enter = false) {
  chrome();
  const c = $('content'), samePage = renderedPage === route.page, sameItem = samePage && renderedId === route.id;
  const listScroll = document.querySelector('.list-pane')?.scrollTop || 0, detailScroll = document.querySelector('.detail-pane')?.scrollTop || 0, pageScroll = c.scrollTop;
  const view = route.page === 'store' ? storePage(enter && !sameItem) : route.page === 'station' ? stationPage(enter && !samePage) : pluginsPage(enter && !sameItem);
  if (!view) return;
  c.replaceChildren(view);
  renderedPage = route.page; renderedId = route.id;
  const lp = document.querySelector('.list-pane'), dp = document.querySelector('.detail-pane');
  if (lp && samePage) lp.scrollTop = listScroll;
  if (dp && sameItem) dp.scrollTop = detailScroll;
  if (samePage && route.page === 'station') c.scrollTop = pageScroll;
  if (!sameItem && !matchMedia('(min-width: 961px)').matches) window.scrollTo(0, 0);
  if (!samePage && lp) lp.querySelector('[aria-current="true"]')?.scrollIntoView({ block: 'center' });
  updateRings(); updatePanels();
}
function updateDiagnostics() {
  for (const t of document.querySelectorAll('[data-plugin-toggle]')) { const p = state?.plugins?.find(p => p.manifest.id === t.dataset.pluginToggle); if (p) t.setAttribute('aria-checked', String(!!p.enabled)); }
  for (const l of document.querySelectorAll('[data-switch-label]')) { const p = state?.plugins?.find(p => p.manifest.id === l.dataset.switchLabel); if (p) l.textContent = p.enabled ? w('включён', 'on') : w('выключен', 'off'); }
  for (const j of document.querySelectorAll('[data-plugin-logs]')) { const p = state?.plugins?.find(p => p.manifest.id === j.dataset.pluginLogs); if (p) { const atEnd = j.scrollTop + j.clientHeight >= j.scrollHeight - 8; j.textContent = logText(p); if (atEnd) j.scrollTop = j.scrollHeight; } }
  for (const f of document.querySelectorAll('[data-plugin-error]')) { const p = state?.plugins?.find(p => p.manifest.id === f.dataset.pluginError); if (p) { f.hidden = !p.last_error; const body = f.querySelector('p'); if (body) body.textContent = p.last_error || ''; } }
  for (const s of document.querySelectorAll('[data-notes]')) { const p = state?.plugins?.find(p => p.manifest.id === s.dataset.notes); if (p) s.replaceChildren(...rowNotes(p)); }
}
async function load() {
  try {
    const [s, y] = await Promise.all([api('/api/state'), api('/api/yam').catch(() => null)]);
    state = s; yam = y; loadError = null; lastYamBusy = !!y?.busy;
  } catch (e) { loadError = e; }
  render();
}
let pollTimer = null;
function schedulePoll(ms) { clearTimeout(pollTimer); pollTimer = setTimeout(poll, ms); }
async function poll() {
  if (!document.hidden && state) {
    try {
      const u = await api('/api/status');
      state.system = u.system; state.updates = u.updates;
      for (const st of u.plugins) { const p = state.plugins?.find(p => p.manifest.id === st.id); if (p) Object.assign(p, st); }
      if (u.yam && yam) {
        yam.jobs = u.yam.jobs; yam.busy = u.yam.busy;
        if (lastYamBusy && !u.yam.busy) {
          lastYamBusy = false; const job = u.yam.jobs[0];
          yam = await api('/api/yam');
          if (job) toast(job.state === 'error' ? w('yam: ошибка — ', 'yam failed: ') + (job.output?.split('\n').pop() || '') : w('yam: готово — ', 'yam: done — ') + (job.action === 'setup' ? 'yam v2' : job.name), job.state === 'error' ? 'danger' : 'ok');
          render();
        } else { lastYamBusy = u.yam.busy; const sp = document.querySelector('[data-setup-progress]'), fresh = setupProgress(); if (sp) { if (fresh) sp.replaceWith(fresh); else sp.remove(); } else if (fresh && route.page !== 'station') render(); for (const b of document.querySelectorAll('[data-job]')) { const j = yam.jobs.find(x => x.id === b.dataset.job); if (j) b.replaceWith(jobBlock(j)); } }
      }
      if (loadError) { loadError = null; await load(); }
      updateDiagnostics();
    } catch (e) { loadError = e; }
  }
  schedulePoll(yam?.busy ? 1000 : 3000);
}
let ringBusy = false;
setInterval(async () => {
  if (ringBusy || document.hidden || !state) return;
  const audio = document.querySelector('[data-audio-panel]');
  if (!ringNeeded() && !audio) return;
  ringBusy = true;
  try { state.ring = await api('/api/ring'); if (audio) { try { state.audio = await api('/api/audio'); } catch { state.audio = null; } } updateRings(); updatePanels(); } catch { } finally { ringBusy = false; }
}, 200);
document.addEventListener('visibilitychange', () => { if (!document.hidden && state) schedulePoll(0); });
let resizeTimer; let wasWide = matchMedia('(min-width: 961px)').matches;
window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => { const wide = matchMedia('(min-width: 961px)').matches; if (wide !== wasWide) { wasWide = wide; render(); } }, 150); });

async function start() {
  parseRoute(); buildChrome(); render(true);
  try { about = await api('/api/about'); } catch (e) { loadError = e; }
  await load(); schedulePoll(3000);
}
start();
