/*
 * better-design · interaction kit (JS, no dependencies)
 *   BetterUI.init(root)                 press/bloom/keyboard feedback for .ui-press, .ui-switch
 *   BetterUI.segmented(el, opts)        sliding indicator for a segmented control or tabs
 *   BetterUI.runWithState(btn, task)    pending → done → idle on a .ui-btn-state button
 *   BetterUI.shake(el)                  one error shake (pair it with visible error text)
 *   BetterUI.swap(el, ms)               icon swap (copy → check) that returns after ms
 *   BetterUI.originFrom(popover, trigger)  grow a popover/menu from its trigger
 * In React/Vue/Svelte call init once at the app root (it uses event delegation), or port
 * the handlers into a shared Button component. Keep the timing rules below.
 */
(function (root) {
  'use strict';

  var MIN_PRESS_MS = 90;   // a tap shorter than this still shows a full press
  var reduceMq = root.matchMedia ? root.matchMedia('(prefers-reduced-motion: reduce)') : { matches: false };
  var clamp = function (v, a, b) { return v < a ? a : v > b ? b : v; };
  var PRESSABLE = '.ui-press, .ui-switch, .ui-dim';

  function disabled(el) { return el.disabled || el.getAttribute('aria-disabled') === 'true'; }

  // Constant visual depth: about 4 px off the perimeter whatever the element's size.
  function pressScale(el) {
    if (el.classList.contains('ui-press--surface')) return 0.985;
    var r = el.getBoundingClientRect();
    return clamp(1 - 8 / (r.width + r.height), 0.9, 0.985).toFixed(4);
  }

  function pressOn(el, x, y) {
    if (disabled(el)) return;
    var r = el.getBoundingClientRect();
    el.style.setProperty('--ui-press-scale', pressScale(el));
    if (x != null) {
      el.style.setProperty('--ui-px', ((x - r.left) / r.width * 100).toFixed(1) + '%');
      el.style.setProperty('--ui-py', ((y - r.top) / r.height * 100).toFixed(1) + '%');
    } else {
      el.style.setProperty('--ui-px', '50%'); el.style.setProperty('--ui-py', '50%');
    }
    el._uiPressAt = performance.now();
    clearTimeout(el._uiPressTimer);
    el.setAttribute('data-pressed', '');
  }
  function pressOff(el) {
    if (!el || !el.hasAttribute('data-pressed')) return;
    var held = performance.now() - (el._uiPressAt || 0);
    var wait = Math.max(0, MIN_PRESS_MS - held);
    clearTimeout(el._uiPressTimer);
    el._uiPressTimer = setTimeout(function () { el.removeAttribute('data-pressed'); }, wait);
  }

  function init(scope) {
    scope = scope || document;
    var active = null;
    function down(e) {
      if (e.pointerType === 'mouse' && e.button !== 0) return;
      var el = e.target.closest && e.target.closest(PRESSABLE);
      if (!el || !scope.contains(el)) return;
      active = el; pressOn(el, e.clientX, e.clientY);
    }
    function up() { pressOff(active); active = null; }
    function key(e) {
      if (e.repeat) return;
      var el = e.target.closest && e.target.closest(PRESSABLE);
      if (!el) return;
      if (e.key === ' ' || e.key === 'Enter') { if (e.type === 'keydown') pressOn(el); else pressOff(el); }
    }
    scope.addEventListener('pointerdown', down, { passive: true });
    root.addEventListener('pointerup', up, { passive: true });
    root.addEventListener('pointercancel', up, { passive: true });
    scope.addEventListener('keydown', key);
    scope.addEventListener('keyup', key);
    scope.addEventListener('focusout', function (e) { var el = e.target.closest && e.target.closest(PRESSABLE); if (el) pressOff(el); });
    return { destroy: function () { scope.removeEventListener('pointerdown', down); root.removeEventListener('pointerup', up); root.removeEventListener('pointercancel', up); scope.removeEventListener('keydown', key); scope.removeEventListener('keyup', key); } };
  }

  /* Segmented control: buttons with aria-selected (tabs) or aria-pressed; one indicator
     element is created and slides to the selected option. Arrow keys move selection. */
  function segmented(el, opts) {
    var o = Object.assign({ onChange: null }, opts || {});
    el.classList.add('ui-segmented');
    var ind = document.createElement('span');
    ind.className = 'ui-segmented__indicator'; ind.setAttribute('aria-hidden', 'true');
    el.insertBefore(ind, el.firstChild);
    var items = Array.prototype.slice.call(el.querySelectorAll(':scope > button'));
    var attr = items[0] && items[0].hasAttribute('aria-pressed') ? 'aria-pressed' : 'aria-selected';
    function place(animate) {
      var cur = items.filter(function (b) { return b.getAttribute(attr) === 'true'; })[0] || items[0];
      if (!cur) return;
      if (!animate) ind.style.transition = 'none';
      el.style.setProperty('--ui-seg-x', cur.offsetLeft + 'px');
      el.style.setProperty('--ui-seg-w', cur.offsetWidth + 'px');
      if (!animate) { void ind.offsetWidth; ind.style.transition = ''; }
    }
    function select(b) {
      items.forEach(function (x) { x.setAttribute(attr, String(x === b)); if (attr === 'aria-selected') x.tabIndex = x === b ? 0 : -1; });
      place(true);
      if (o.onChange) o.onChange(items.indexOf(b), b);
    }
    items.forEach(function (b) { b.addEventListener('click', function () { select(b); }); });
    el.addEventListener('keydown', function (e) {
      var d = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
      if (!d) return;
      var i = items.findIndex(function (x) { return x.getAttribute(attr) === 'true'; });
      var n = items[(i + d + items.length) % items.length];
      e.preventDefault(); select(n); n.focus();
    });
    var ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(function () { place(false); }) : null;
    if (ro) ro.observe(el);
    place(false);
    return { select: function (i) { select(items[i]); }, destroy: function () { if (ro) ro.disconnect(); ind.remove(); } };
  }

  /* Button states. The button keeps its width, ignores repeat presses while pending,
     announces busy state, shows done for doneMs, then returns. task returns a promise. */
  function runWithState(btn, task, opts) {
    var o = Object.assign({ doneMs: 1600, onError: null }, opts || {});
    var box = btn.querySelector('.ui-btn-state') || btn;
    if (box.getAttribute('data-state') === 'pending') return Promise.resolve();
    btn.style.minWidth = btn.getBoundingClientRect().width + 'px';
    box.setAttribute('data-state', 'pending');
    btn.setAttribute('aria-busy', 'true');
    return Promise.resolve().then(task).then(function (v) {
      box.setAttribute('data-state', 'done');
      btn.removeAttribute('aria-busy');
      setTimeout(function () { box.removeAttribute('data-state'); btn.style.minWidth = ''; }, o.doneMs);
      return v;
    }, function (err) {
      box.removeAttribute('data-state'); btn.removeAttribute('aria-busy'); btn.style.minWidth = '';
      shake(btn);
      if (o.onError) o.onError(err);
      throw err;
    });
  }

  function shake(el) {
    if (reduceMq.matches) return;
    el.classList.remove('ui-shake'); void el.offsetWidth; el.classList.add('ui-shake');
    el.addEventListener('animationend', function () { el.classList.remove('ui-shake'); }, { once: true });
  }

  function swap(el, ms) {
    el.setAttribute('data-swapped', '');
    clearTimeout(el._uiSwap);
    if (ms) el._uiSwap = setTimeout(function () { el.removeAttribute('data-swapped'); }, ms);
  }

  // Sets transform-origin so a popover/menu grows out of the control that opened it.
  function originFrom(pop, trigger) {
    var p = pop.getBoundingClientRect(), t = trigger.getBoundingClientRect();
    var x = clamp(t.left + t.width / 2 - p.left, 0, p.width), y = t.top + t.height / 2 < p.top ? 0 : p.height;
    pop.style.setProperty('--ui-origin', x + 'px ' + y + 'px');
  }

  var api = root.BetterUI = root.BetterUI || {};
  api.init = init; api.segmented = segmented; api.runWithState = runWithState;
  api.shake = shake; api.swap = swap; api.originFrom = originFrom; api.pressScale = pressScale;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : window);
