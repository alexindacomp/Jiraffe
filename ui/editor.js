/* Jiraffe UI: rich-text editor for HTML (JEditor) descriptions (SPEC v0.5), no libraries.
   Exposes window.JiraffeEditor. The document model is the v0.4 sanitizer's safe subset:
     blocks  p h3 h4 pre blockquote ul ol li table thead tbody tr td th hr, panel blocks
             (div.desc-panel[data-variant] > div.desc-panel-head + div.desc-panel-body)
     inline  strong em u s code a[data-href] br, image chips (span.ed-img[contenteditable=false][data-src…])
   Content only ever enters the live editor through the injected v0.4 sanitizer (loading, paste, drop) or through
   the editor's own commands; after every change the DOM is normalized back into the model. The serializer writes
   HTML strings with full escaping (no innerHTML read-back), panels as JEditor markup and chips as image-wrap <img>. */
(function () {
  'use strict';

  /** http(s) URL as the backend validator accepts it: parses, has a host, no user info, no broken %-escapes (S-F2). */
  function isHttpUrl(v) {
    const str = String(v || '');
    if (!/^https?:\/\/[^\s"'<>]+$/i.test(str) || /%(?![0-9a-f]{2})/i.test(str)) return false;
    try {
      const u = new URL(str);
      return (u.protocol === 'http:' || u.protocol === 'https:') && !!u.hostname && !u.username && !u.password;
    } catch { return false; }
  }
  const HTTP = { test: isHttpUrl };
  /** Image width / height: an integer 1–100000 without leading zeros, else '' (S-F2). */
  function dimension(v) {
    if (!/^\s*\d{1,6}\s*$/.test(String(v == null ? '' : v))) return '';
    const n = parseInt(v, 10);
    return n >= 1 && n <= 100000 ? String(n) : '';
  }
  /** Integer attribute as the validator accepts it (no leading zeros, in range), else '' (DR-18). */
  function intAttr(v, max) {
    const str = String(v == null ? '' : v).trim();
    if (!/^[1-9]\d{0,5}$/.test(str)) return '';
    return Number(str) <= max ? str : '';
  }
  /** Copies the validated table-cell / list attributes the model keeps; the rest is stripped. */
  function keepAttrs(n) {
    const keep = {};
    if (n.nodeName === 'TD' || n.nodeName === 'TH') ['colspan', 'rowspan'].forEach((a) => { const v = intAttr(n.getAttribute(a), 50); if (v && v !== '1') keep[a] = v; });
    if (n.nodeName === 'OL') { const v = intAttr(n.getAttribute('start'), 100000); if (v && v !== '1') keep.start = v; }
    stripAttrs(n);
    Object.keys(keep).forEach((a) => n.setAttribute(a, keep[a]));
  }
  const C0 = /[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/g; // control characters (not \t \n \r): never written (S-F1)
  const MAX_PASTE_HTML = 1024 * 1024; // larger clipboard HTML is pasted as plain text (S-F4)
  const MAX_LIST_DEPTH = 20;          // nested lists (the backend allows a nesting depth of 64)
  const VARIANTS = ['info', 'warning', 'neutral'];
  const RENAME = { B: 'strong', I: 'em', STRIKE: 's', DEL: 's', INS: 'u', H1: 'h3', H2: 'h3', H5: 'h4', H6: 'h4', TFOOT: 'tbody' };
  const INLINE_TAGS = new Set(['STRONG', 'EM', 'U', 'S', 'CODE', 'A']);
  const FLOW_BLOCKS = new Set(['P', 'H3', 'H4', 'PRE', 'BLOCKQUOTE', 'UL', 'OL', 'TABLE', 'HR']);
  const DROP_TAGS = new Set(['IMG', 'SCRIPT', 'STYLE', 'IFRAME', 'OBJECT', 'EMBED', 'SVG', 'MATH', 'TEMPLATE', 'INPUT', 'BUTTON', 'SELECT', 'TEXTAREA', 'FORM', 'VIDEO', 'AUDIO', 'CANVAS', 'META', 'LINK', 'TITLE']);
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = (s) => String(s).replace(C0, '').replace(/[&<>"']/g, (c) => ESC[c]).replace(/\u00a0/g, '&nbsp;');

  /* JEditor panels: the exact class + style strings of the SPEC "HTML description contract" (the server-side
     validator accepts nothing else). Neutral panels are written as info panels. */
  const PANEL = {
    info: { cls: 'jePanel_blue', head: 'jePanelHeader', style: 'background-color:#e0efff; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em', headStyle: 'border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center' },
    warning: { cls: 'jePanel_yell', head: 'jePanelHeader_red', style: 'background-color:#ffffce; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em', headStyle: 'background-color:#f7d6c1; border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center' },
  };
  const PANEL_CONTENT_STYLE = 'padding:.5em 2em .5em 2em';

  const el = (tag, cls) => { const e = document.createElement(tag); if (cls) e.className = cls; return e; };
  /** <svg class="icon"><use href="#i-NAME"></use></svg> from the app's sprite. */
  function svgIcon(name, cls) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon' + (cls ? ' ' + cls : ''));
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#i-' + name);
    svg.appendChild(use);
    return svg;
  }
  const isEl = (n, tag) => !!n && n.nodeType === 1 && (!tag || n.nodeName === tag);
  const isChip = (n) => isEl(n, 'SPAN') && n.classList.contains('ed-img');
  const isPanel = (n) => isEl(n, 'DIV') && n.classList.contains('desc-panel');
  const isPanelHead = (n) => isEl(n, 'DIV') && n.classList.contains('desc-panel-head');
  const isPanelBody = (n) => isEl(n, 'DIV') && n.classList.contains('desc-panel-body');
  const isBlankText = (n) => n.nodeType === 3 && !/[^\s\u00a0]/.test(n.data);
  const isInlineNode = (n) => n.nodeType === 3 || isEl(n, 'BR') || isChip(n) || (n.nodeType === 1 && INLINE_TAGS.has(n.nodeName));
  const hasContent = (n) => /[^\s\u00a0]/.test(n.textContent) || !!(n.querySelector && n.querySelector('.ed-img, hr, table, br'));
  const hasText = (n) => /[^\s\u00a0]/.test(n.textContent) || !!(n.querySelector && n.querySelector('.ed-img'));
  function unwrap(n) { const p = n.parentNode; while (n.firstChild) p.insertBefore(n.firstChild, n); p.removeChild(n); }
  function rename(n, tag) {
    const r = document.createElement(tag);
    while (n.firstChild) r.appendChild(n.firstChild);
    n.parentNode.replaceChild(r, n);
    return r;
  }
  function stripAttrs(n, keep) {
    [...n.attributes].forEach((a) => { if (!keep || !keep.includes(a.name)) n.removeAttribute(a.name); });
  }

  /* ---------------- model normalization ---------------- */

  /** Image chip: validated data-* only; the visible content is rebuilt (never trusted). */
  function fixChip(chip, t) {
    const d = {};
    const src = chip.getAttribute('data-src');
    if (src && HTTP.test(src)) d.src = src;
    const alt = chip.getAttribute('data-alt');
    if (alt) d.alt = alt.slice(0, 200);
    ['width', 'height'].forEach((k) => { const v = dimension(chip.getAttribute('data-' + k)); if (v) d[k] = v; });
    stripAttrs(chip);
    chip.className = 'ed-img';
    chip.setAttribute('contenteditable', 'false');
    Object.keys(d).forEach((k) => chip.setAttribute('data-' + k, d[k]));
    const label = d.alt ? t('drawer.imageAlt', { alt: d.alt }) : t('drawer.image');
    if (chip.dataset.label === label && chip.childNodes.length === 2) return;
    chip.dataset.label = label;
    chip.textContent = '';
    const span = el('span');
    span.textContent = label;
    chip.append(svgIcon('image', 'icon-sm'), span);
  }

  /** Pass 1: tags and attributes, recursively (structure is fixed in pass 2). */
  function cleanTree(node, t) {
    let n = node.firstChild;
    while (n) {
      const next = n.nextSibling;
      if (n.nodeType === 3) { n = next; continue; }
      if (n.nodeType !== 1) { node.removeChild(n); n = next; continue; }
      if (isChip(n)) { fixChip(n, t); n = next; continue; }
      let tag = n.nodeName;
      if (DROP_TAGS.has(tag) || n.namespaceURI !== 'http://www.w3.org/1999/xhtml') { node.removeChild(n); n = next; continue; }
      if (RENAME[tag]) { n = rename(n, RENAME[tag]); tag = n.nodeName; }
      if (tag === 'A') {
        const href = n.getAttribute('data-href') || n.getAttribute('href') || '';
        if (!HTTP.test(href.trim())) { const first = n.firstChild; unwrap(n); n = first || next; continue; }
        stripAttrs(n);
        n.setAttribute('data-href', href.trim());
      } else if (tag === 'DIV' && (isPanel(n) || isPanelHead(n) || isPanelBody(n))) {
        stripAttrs(n, isPanel(n) ? ['class', 'data-variant'] : ['class']);
        n.className = isPanel(n) ? 'desc-panel' : isPanelHead(n) ? 'desc-panel-head' : 'desc-panel-body';
        if (isPanel(n) && !VARIANTS.includes(n.getAttribute('data-variant'))) n.setAttribute('data-variant', 'neutral');
      } else if (FLOW_BLOCKS.has(tag) || INLINE_TAGS.has(tag) || ['BR', 'LI', 'THEAD', 'TBODY', 'TR', 'TD', 'TH', 'DIV'].includes(tag)) {
        keepAttrs(n);
      } else {
        // span, font and anything unknown: unwrapped (text kept)
        const first = n.firstChild;
        unwrap(n);
        n = first || next;
        continue;
      }
      cleanTree(n, t);
      n = next;
    }
  }

  /** Inline content only (paragraphs, headings, list items, cells, panel heads): blocks are flattened with <br>. */
  function flattenBlocks(container, allowLists) {
    let n = container.firstChild;
    while (n) {
      const next = n.nextSibling;
      if (n.nodeType === 1 && !isInlineNode(n) && !(allowLists && (n.nodeName === 'UL' || n.nodeName === 'OL'))) {
        if (n.nodeName === 'HR') { container.replaceChild(el('br'), n); n = next; continue; }
        if (n.nodeName === 'PRE') { const s = document.createTextNode(n.textContent); container.replaceChild(s, n); n = next; continue; }
        if (n.previousSibling && !isEl(n.previousSibling, 'BR')) container.insertBefore(el('br'), n);
        const first = n.firstChild;
        unwrap(n);
        n = first || next;
        continue;
      }
      n = next;
    }
  }

  /** Removes empty / redundant inline elements inside an inline container. */
  function tidyInline(container) {
    container.querySelectorAll('strong, em, u, s, code, a').forEach((x) => {
      if (!x.isConnected) return;
      if (!/[^\s\u00a0]/.test(x.textContent) && !x.querySelector('br, .ed-img')) { x.remove(); return; }
      const outer = x.parentElement && x.parentElement.closest(x.nodeName.toLowerCase());
      if (outer && container.contains(outer)) unwrap(x); // <strong> inside <strong>, <a> inside <a>
    });
    container.querySelectorAll('code .ed-img').forEach((c) => c.closest('code').after(c));
  }
  const ensureLine = (b) => { if (!hasContent(b)) { b.textContent = ''; b.appendChild(el('br')); } };

  function fixList(list) {
    let n = list.firstChild;
    while (n) {
      const next = n.nextSibling;
      if (isEl(n, 'LI')) { n = next; continue; }
      if (isBlankText(n)) { list.removeChild(n); n = next; continue; }
      if (isEl(n, 'UL') || isEl(n, 'OL')) {
        let prev = n.previousSibling;
        if (!isEl(prev, 'LI')) { prev = el('li'); list.insertBefore(prev, n); }
        prev.appendChild(n);
        n = next;
        continue;
      }
      const li = el('li');
      list.insertBefore(li, n);
      li.appendChild(n);
      while (li.nextSibling && li.nextSibling !== next && isInlineNode(li.nextSibling)) li.appendChild(li.nextSibling);
      n = li.nextSibling;
    }
    [...list.children].forEach((li) => {
      flattenBlocks(li, true);
      [...li.children].filter((c) => c.nodeName === 'UL' || c.nodeName === 'OL').forEach(fixList);
      tidyInline(li);
      if (!hasContent(li)) { li.textContent = ''; li.appendChild(el('br')); }
    });
    if (!list.children.length) list.remove();
  }

  function fixTable(table) {
    [...table.childNodes].forEach((c) => {
      if (isEl(c, 'TR')) { let body = c.previousElementSibling; if (!isEl(body, 'TBODY')) { body = el('tbody'); table.insertBefore(body, c); } body.appendChild(c); } else if (!isEl(c, 'THEAD') && !isEl(c, 'TBODY')) c.remove();
    });
    table.querySelectorAll('thead, tbody').forEach((sec) => {
      [...sec.childNodes].forEach((r) => { if (!isEl(r, 'TR')) r.remove(); });
      [...sec.children].forEach((tr) => {
        [...tr.childNodes].forEach((c) => {
          if (isEl(c, 'TD') || isEl(c, 'TH')) return;
          if (isBlankText(c)) { c.remove(); return; }
          const td = el('td'); tr.insertBefore(td, c); td.appendChild(c);
        });
        if (!tr.children.length) tr.remove();
        [...tr.children].forEach((cell) => { flattenBlocks(cell, false); tidyInline(cell); ensureLine(cell); });
      });
      if (!sec.children.length) sec.remove();
    });
    if (!table.querySelector('tr')) table.remove();
  }

  function fixPanel(panel, t) {
    let head = [...panel.children].find(isPanelHead);
    let body = [...panel.children].find(isPanelBody);
    if (!head) { head = el('div', 'desc-panel-head'); head.textContent = t(panel.getAttribute('data-variant') === 'warning' ? 'editor.panelWarningHead' : 'editor.panelInfoHead'); }
    if (!body) body = el('div', 'desc-panel-body');
    [...panel.childNodes].forEach((c) => { if (c !== head && c !== body) body.appendChild(c); });
    panel.prepend(head);
    head.after(body);
    flattenBlocks(head, false);
    head.querySelectorAll('br').forEach((b) => b.remove());
    tidyInline(head);
    if (!hasContent(head)) { head.textContent = ''; head.appendChild(el('br')); }
    fixFlow(body, false, t);
  }

  /** Flow container (editor root, panel body, blockquote): only blocks; inline runs become paragraphs. */
  function fixFlow(container, allowPanels, t) {
    let n = container.firstChild;
    while (n) {
      const next = n.nextSibling;
      if (isBlankText(n)) {
        // whitespace between blocks (or at the edges) is formatting, not content; between inline nodes it is a space
        const sib = (dir) => { let x = n[dir]; while (x && isBlankText(x)) x = x[dir]; return x; };
        const before = sib('previousSibling'); const after = sib('nextSibling');
        if (!before || !after || !isInlineNode(before) || !isInlineNode(after)) { container.removeChild(n); n = next; continue; }
      }
      if (isInlineNode(n)) {
        const p = el('p');
        container.insertBefore(p, n);
        while (p.nextSibling && isInlineNode(p.nextSibling)) p.appendChild(p.nextSibling);
        n = p.nextSibling;
        continue;
      }
      if (isPanel(n) && !allowPanels) {
        // panels only live at the top level: a nested one is unwrapped into its header line + body blocks
        const prevSib = n.previousSibling;
        const body = [...n.children].find(isPanelBody);
        if (body) n.after(...body.childNodes);
        [...n.children].filter(isPanelHead).forEach((h) => { const p = el('p'); p.append(...h.childNodes); n.before(p); });
        n.remove();
        n = prevSib ? prevSib.nextSibling : container.firstChild;
        continue;
      }
      if (isEl(n, 'DIV') && !isPanel(n)) {
        // plain div (pasted / browser-made): a paragraph if it only holds inline content, else unwrapped
        if ([...n.childNodes].every(isInlineNode)) { n = rename(n, 'p'); } else { const first = n.firstChild; unwrap(n); n = first || next; continue; }
      }
      if (isEl(n, 'LI')) { const ul = el('ul'); container.insertBefore(ul, n); while (ul.nextSibling && isEl(ul.nextSibling, 'LI')) ul.appendChild(ul.nextSibling); n = ul; }
      if (['TR', 'TD', 'TH', 'THEAD', 'TBODY'].includes(n.nodeName)) { const p = el('p'); p.textContent = n.textContent; container.replaceChild(p, n); n = p; }
      const prev = n.previousSibling;
      switch (n.nodeName) {
        case 'P': case 'H3': case 'H4':
          flattenBlocks(n, false); tidyInline(n); ensureLine(n); break;
        case 'PRE':
          if ([...n.childNodes].some((c) => c.nodeType !== 3)) { const s = [...n.childNodes].map((c) => (isEl(c, 'BR') ? '\n' : c.textContent)).join(''); n.textContent = s; }
          if (!n.textContent) n.textContent = '\n';
          break;
        case 'BLOCKQUOTE': fixFlow(n, false, t); if (!n.firstChild) n.appendChild(el('p')).appendChild(el('br')); break;
        case 'UL': case 'OL': fixList(n); break;
        case 'TABLE': fixTable(n); break;
        case 'HR': n.textContent = ''; break;
        default:
          if (isPanel(n)) fixPanel(n, t);
      }
      // continue after the processed block (it may have been removed, or may have absorbed its siblings)
      n = n.parentNode === container ? n.nextSibling : prev ? prev.nextSibling : container.firstChild;
    }
  }

  /** Brings any DOM under `root` back into the model. Mutates only what is off-model. */
  function normalize(root, t) {
    cleanTree(root, t);
    fixFlow(root, true, t);
    // a second flow pass catches nodes moved by the first one (e.g. unwrapped divs)
    fixFlow(root, true, t);
    if (!root.firstChild) { const p = el('p'); p.appendChild(el('br')); root.appendChild(p); }
  }

  /* ---------------- serializer ---------------- */

  function serializeInline(parent) {
    let out = '';
    parent.childNodes.forEach((n) => {
      if (n.nodeType === 3) { out += esc(n.data); return; }
      if (n.nodeType !== 1) return;
      if (isChip(n)) {
        const src = n.getAttribute('data-src');
        if (!src || !HTTP.test(src)) return; // not http(s): dropped
        let attrs = ` src="${esc(src)}"`;
        if (n.hasAttribute('data-alt')) attrs += ` alt="${esc(n.getAttribute('data-alt'))}"`;
        ['width', 'height'].forEach((k) => { const v = dimension(n.getAttribute('data-' + k)); if (v) attrs += ` ${k}="${v}"`; });
        out += `<span class="image-wrap"><img${attrs}></span>`;
        return;
      }
      switch (n.nodeName) {
        case 'BR': out += '<br>'; break;
        case 'STRONG': case 'EM': case 'U': case 'S': case 'CODE': {
          const tag = n.nodeName.toLowerCase();
          out += `<${tag}>${serializeInline(n)}</${tag}>`;
          break;
        }
        case 'A': {
          const href = n.getAttribute('data-href') || '';
          out += HTTP.test(href) ? `<a href="${esc(href)}">${serializeInline(n)}</a>` : serializeInline(n);
          break;
        }
        default: out += serializeInline(n); // never reached for a normalized tree
      }
    });
    return out;
  }
  function serializeList(list) {
    const tag = list.nodeName.toLowerCase();
    const start = tag === 'ol' ? intAttr(list.getAttribute('start'), 100000) : '';
    let out = start && start !== '1' ? `<ol start="${start}">` : `<${tag}>`;
    [...list.children].forEach((li) => {
      let inner = '';
      let run = el('span');
      const flush = () => { inner += serializeInline(run); run = el('span'); };
      [...li.childNodes].forEach((c) => {
        if (isEl(c, 'UL') || isEl(c, 'OL')) { flush(); inner += serializeList(c); } else run.appendChild(c.cloneNode(true));
      });
      flush();
      out += `<li>${inner}</li>`;
    });
    return out + `</${tag}>`;
  }
  function serializeBlocks(container) {
    let out = '';
    [...container.children].forEach((n) => {
      switch (n.nodeName) {
        case 'P': { const s = serializeInline(n); out += `<p dir="auto">${s === '<br>' || s === '' ? '&nbsp;' : s}</p>`; break; }
        case 'H3': case 'H4': { const tag = n.nodeName.toLowerCase(); out += `<${tag}>${serializeInline(n)}</${tag}>`; break; }
        case 'PRE': out += `<pre>${esc(n.textContent.replace(/\n$/, ''))}</pre>`; break;
        case 'BLOCKQUOTE': out += `<blockquote>${serializeBlocks(n)}</blockquote>`; break;
        case 'UL': case 'OL': out += serializeList(n); break;
        case 'HR': out += '<hr>'; break;
        case 'TABLE': {
          out += '<table>';
          [...n.children].forEach((sec) => {
            const tag = sec.nodeName.toLowerCase();
            out += `<${tag}>`;
            [...sec.children].forEach((tr) => {
              out += '<tr>';
              [...tr.children].forEach((cell) => {
                const ct = cell.nodeName.toLowerCase();
                const sp = ['colspan', 'rowspan'].map((a) => { const v = intAttr(cell.getAttribute(a), 50); return v && v !== '1' ? ` ${a}="${v}"` : ''; }).join('');
                const s1 = serializeInline(cell);
                out += `<${ct}${sp}>${s1 === '<br>' ? '' : s1}</${ct}>`;
              });
              out += '</tr>';
            });
            out += `</${tag}>`;
          });
          out += '</table>';
          break;
        }
        default:
          if (isPanel(n)) {
            const v = n.getAttribute('data-variant') === 'warning' ? PANEL.warning : PANEL.info;
            const head = [...n.children].find(isPanelHead);
            const body = [...n.children].find(isPanelBody);
            const htext = head ? serializeInline(head).replace(/^<br>$/, '') : '';
            out += `<div class="${v.cls}" style="${v.style}"><div class="${v.head}" style="${v.headStyle}">${htext}</div><div class="jePanelContent" style="${PANEL_CONTENT_STYLE}">${body ? serializeBlocks(body) : ''}</div></div>`;
          }
      }
    });
    return out;
  }
  /** The editor model → clean HTML for Jira ("" when the document is empty). */
  function serialize(root) {
    if (isEmptyDoc(root)) return '';
    return serializeBlocks(root);
  }
  function isEmptyDoc(root) {
    return !/[^\s\u00a0]/.test(root.textContent) && !root.querySelector('.ed-img, hr, table');
  }

  /* ---------------- selection helpers ---------------- */

  function pathOf(root, node) {
    const p = [];
    for (let x = node; x && x !== root; x = x.parentNode) {
      if (!x.parentNode) return null;
      p.unshift([...x.parentNode.childNodes].indexOf(x));
    }
    return p;
  }
  function nodeAt(root, path) {
    let x = root;
    for (const i of path || []) { if (!x || !x.childNodes[i]) return null; x = x.childNodes[i]; }
    return x;
  }

  /* ---------------- the editor ---------------- */

  /**
   * create({ host, label, describedBy, t, sanitize, onChange, onNotice })
   *   sanitize(html) → { root: Element (editor-model nodes), lossy: bool }  — the v0.4 sanitizer, editor mode
   */
  function create(opts) {
    const { host, t } = opts;
    const uid = 'rte' + Math.random().toString(36).slice(2, 8);
    host.classList.add('rte');
    host.textContent = '';

    // Toolbar groups; ctx = only shown in that context.
    const BUTTONS = [
      ['h3', 'heading-3', 'editor.h3', true], ['h4', 'heading-4', 'editor.h4', true], '|',
      ['bold', 'bold', 'editor.bold', true, 'Ctrl+B'], ['italic', 'italic', 'editor.italic', true, 'Ctrl+I'], ['underline', 'underline', 'editor.underline', true, 'Ctrl+U'],
      ['strike', 'strike', 'editor.strike', true], ['code', 'code', 'editor.code', true], '|',
      ['ul', 'list-ul', 'editor.ul', true], ['ol', 'list-ol', 'editor.ol', true], ['outdent', 'outdent', 'editor.outdent', false, 'Shift+Tab'], ['indent', 'indent', 'editor.indent', false, 'Tab'], '|',
      ['link', 'link', 'editor.link', true, 'Ctrl+K'], ['pre', 'code-block', 'editor.pre', true], ['quote', 'quote', 'editor.quote', true], ['hr', 'minus', 'editor.hr'],
      ['table', 'table', 'editor.table'], ['panelInfo', 'info', 'editor.panelInfo'], ['panelWarning', 'alert', 'editor.panelWarning'], '|',
      ['clear', 'eraser', 'editor.clear'], ['undo', 'undo', 'editor.undo', false, 'Ctrl+Z'], ['redo', 'redo', 'editor.redo', false, 'Ctrl+Y'],
    ];
    const CONTEXT = [
      ['rowAdd', 'editor.rowAdd', 'table'], ['rowDel', 'editor.rowDel', 'table'], ['colAdd', 'editor.colAdd', 'table'], ['colDel', 'editor.colDel', 'table'],
      ['panelRemove', 'editor.panelRemove', 'panel'],
    ];
    const bar = el('div', 'rte-toolbar');
    bar.setAttribute('role', 'toolbar');
    bar.setAttribute('aria-label', t('editor.toolbar'));
    bar.setAttribute('aria-controls', uid);
    const btn = {};
    BUTTONS.forEach((b) => {
      if (b === '|') { const s = el('span', 'rte-sep'); s.setAttribute('aria-hidden', 'true'); bar.appendChild(s); return; }
      const [cmd, ic, key, toggle, kbd] = b;
      const x = el('button', 'rte-btn');
      x.type = 'button';
      x.dataset.cmd = cmd;
      x.tabIndex = -1;
      const label = t(key) + (kbd ? ` (${kbd})` : '');
      x.setAttribute('aria-label', label);
      x.title = label;
      if (toggle) x.setAttribute('aria-pressed', 'false');
      x.appendChild(svgIcon(ic));
      bar.appendChild(x);
      btn[cmd] = x;
    });
    const ctx = el('div', 'rte-context');
    CONTEXT.forEach(([cmd, key, where]) => {
      const x = el('button', 'rte-btn rte-btn-text');
      x.type = 'button';
      x.dataset.cmd = cmd;
      x.dataset.ctx = where;
      x.tabIndex = -1;
      x.textContent = t(key);
      x.hidden = true;
      ctx.appendChild(x);
      btn[cmd] = x;
    });
    bar.appendChild(ctx);
    btn.h3.tabIndex = 0;

    // Link form (inline, under the toolbar)
    const linkForm = el('div', 'rte-linkform');
    linkForm.hidden = true;
    const mkField = (labelKey, type) => {
      const lab = el('label', 'rte-lf-field');
      const span = el('span'); span.textContent = t(labelKey);
      const inp = el('input', 'input'); inp.type = type; inp.spellcheck = false; inp.autocomplete = 'off';
      lab.append(span, inp);
      return [lab, inp];
    };
    const [lfText, lfTextIn] = mkField('editor.linkText', 'text');
    const [lfUrl, lfUrlIn] = mkField('editor.linkUrl', 'url');
    lfUrlIn.placeholder = 'https://';
    const lfErr = el('p', 'field-error'); lfErr.hidden = true; lfErr.id = uid + '-lferr';
    lfUrlIn.setAttribute('aria-describedby', lfErr.id);
    const lfApply = el('button', 'btn btn-sm btn-primary'); lfApply.type = 'button'; lfApply.textContent = t('editor.linkApply');
    const lfRemove = el('button', 'btn btn-sm'); lfRemove.type = 'button'; lfRemove.textContent = t('editor.linkRemove');
    const lfCancel = el('button', 'btn btn-sm btn-ghost'); lfCancel.type = 'button'; lfCancel.textContent = t('common.cancel');
    const lfActions = el('div', 'rte-lf-actions'); lfActions.append(lfApply, lfRemove, lfCancel);
    linkForm.append(lfText, lfUrl, lfActions, lfErr);

    const content = el('div', 'rte-content wiki wiki-html');
    content.id = uid;
    content.contentEditable = 'true';
    content.setAttribute('role', 'textbox');
    content.setAttribute('aria-multiline', 'true');
    content.setAttribute('aria-label', opts.label);
    content.spellcheck = true;
    if (opts.describedBy) content.setAttribute('aria-describedby', opts.describedBy);
    host.append(bar, linkForm, content);

    let lastRange = null;
    // DR-19: inline toggles with a collapsed caret apply to the next typed text (cleared when the caret moves)
    const MARKS = { bold: 'strong', italic: 'em', underline: 'u', strike: 's', code: 'code' };
    const pending = new Map(); // cmd → true (apply to the next text) | false (end the mark for the next text)
    let pendingAt = null;
    function togglePending(cmd) {
      const r = selRange();
      const inside = !!(r && closestIn(r.startContainer, MARKS[cmd]));
      const now = pending.has(cmd) ? pending.get(cmd) : inside;
      if (!now === inside) pending.delete(cmd); else pending.set(cmd, !now); // back to the caret's own state ⇒ nothing pending
      pendingAt = r ? [r.startContainer, r.startOffset] : null;
      btn[cmd].setAttribute('aria-pressed', !now ? 'true' : 'false');
      updateState();
    }
    /** Inserts typed text with the pending marks: wraps it in the "on" marks and splits out of the "off" ones. */
    function insertWithMarks(text) {
      const r = selRange();
      if (!r) return false;
      r.deleteContents();
      let node = document.createTextNode(text.replace(C0, ''));
      const textNode = node;
      let at = r.cloneRange();
      // turn off: split the enclosing mark element at the caret and insert between the halves
      [...pending].filter(([, want]) => !want).forEach(([cmd]) => {
        const elx = closestIn(at.startContainer, MARKS[cmd]);
        if (!elx) return;
        const tail = document.createRange();
        tail.setStart(at.startContainer, at.startOffset);
        tail.setEnd(elx, elx.childNodes.length);
        const after = elx.cloneNode(false);
        after.appendChild(tail.extractContents());
        elx.after(after);
        if (!hasContent(after)) after.remove();
        at = document.createRange();
        at.setStartAfter(elx);
        at.collapse(true);
      });
      // turn on: wrap the new text
      [...pending].filter(([, want]) => want).forEach(([cmd]) => {
        if (closestIn(at.startContainer, MARKS[cmd])) return;
        const w = el(MARKS[cmd]);
        w.appendChild(node);
        node = w;
      });
      at.insertNode(node);
      pending.clear();
      pendingAt = null;
      const rr = document.createRange();
      rr.setStart(textNode, textNode.data.length);
      rr.collapse(true);
      const sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(rr);
      lastRange = rr.cloneRange();
      return true;
    }
    let history = [];
    let hpos = -1;
    let lastCommitAt = 0;
    let lastKind = '';
    let baseline = '';
    let loading = false;

    const inEditor = (n) => !!n && content.contains(n);
    const selRange = () => {
      const s = window.getSelection();
      if (s && s.rangeCount && inEditor(s.getRangeAt(0).commonAncestorContainer)) return s.getRangeAt(0);
      return null;
    };
    function restoreRange() {
      const s = window.getSelection();
      if (lastRange && inEditor(lastRange.startContainer)) { s.removeAllRanges(); s.addRange(lastRange); return lastRange; }
      const r = document.createRange();
      r.selectNodeContents(content.lastElementChild || content);
      r.collapse(false);
      s.removeAllRanges(); s.addRange(r);
      return r;
    }
    function placeCaret(node, atEnd) {
      const r = document.createRange();
      if (!node) return;
      if (node.nodeType === 3) r.setStart(node, atEnd ? node.data.length : 0);
      else { r.selectNodeContents(node); r.collapse(!atEnd); }
      const s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
      lastRange = r.cloneRange();
    }

    /* history: snapshots of the (normalized) model as cloned nodes + caret path */
    function snapshot() {
      const s = window.getSelection();
      let sel = null;
      if (s && s.rangeCount && inEditor(s.anchorNode)) sel = { a: pathOf(content, s.anchorNode), ao: s.anchorOffset, f: pathOf(content, s.focusNode), fo: s.focusOffset };
      return { nodes: [...content.childNodes].map((n) => n.cloneNode(true)), sel };
    }
    function restore(snap) {
      content.replaceChildren(...snap.nodes.map((n) => n.cloneNode(true)));
      if (snap.sel) {
        const a = nodeAt(content, snap.sel.a); const f = nodeAt(content, snap.sel.f);
        if (a && f) {
          try { window.getSelection().setBaseAndExtent(a, Math.min(snap.sel.ao, a.nodeType === 3 ? a.data.length : a.childNodes.length), f, Math.min(snap.sel.fo, f.nodeType === 3 ? f.data.length : f.childNodes.length)); } catch { /* ignore */ }
        }
      }
    }
    function commit(kind) {
      const now = Date.now();
      const snap = snapshot();
      if (kind === 'typing' && lastKind === 'typing' && now - lastCommitAt < 800 && hpos >= 0) history[hpos] = snap;
      else { history = history.slice(0, hpos + 1); history.push(snap); if (history.length > 100) history.shift(); hpos = history.length - 1; }
      lastCommitAt = now;
      lastKind = kind;
      updateState();
      if (opts.onChange && !loading) opts.onChange();
    }
    function undo() { if (hpos > 0) { hpos--; restore(history[hpos]); normalizeLive(); lastKind = ''; updateState(); if (opts.onChange) opts.onChange(); } }
    function redo() { if (hpos < history.length - 1) { hpos++; restore(history[hpos]); normalizeLive(); lastKind = ''; updateState(); if (opts.onChange) opts.onChange(); } }

    /** Normalizes the live DOM keeping the caret when its nodes survive. */
    function normalizeLive() {
      const s = window.getSelection();
      const keep = s && s.rangeCount && inEditor(s.anchorNode) ? [s.anchorNode, s.anchorOffset, s.focusNode, s.focusOffset] : null;
      normalize(content, t);
      if (keep && keep[0].isConnected && keep[2].isConnected && inEditor(keep[0]) && inEditor(keep[2])) {
        const lim = (n, o) => Math.min(o, n.nodeType === 3 ? n.data.length : n.childNodes.length);
        try { s.setBaseAndExtent(keep[0], lim(keep[0], keep[1]), keep[2], lim(keep[2], keep[3])); } catch { /* ignore */ }
      }
    }

    /* contexts */
    const closestIn = (node, sel) => { const e = node && (node.nodeType === 1 ? node : node.parentElement); const x = e && e.closest(sel); return x && inEditor(x) ? x : null; };
    /** The block that is a direct child of a flow container (root, panel body, blockquote), and that container. */
    function blockOf(node) {
      let x = node && node.nodeType === 3 ? node.parentNode : node;
      while (x && x !== content) {
        const p = x.parentNode;
        if (p === content || isPanelBody(p) || isEl(p, 'BLOCKQUOTE')) return { block: x, container: p };
        x = p;
      }
      return { block: null, container: content };
    }
    function topBlockOf(node) {
      let x = node && node.nodeType === 3 ? node.parentNode : node;
      while (x && x.parentNode !== content) x = x.parentNode;
      return x && x.parentNode === content ? x : null;
    }

    /* toolbar state */
    let stateQueued = false;
    function updateState() {
      if (stateQueued) return;
      stateQueued = true;
      requestAnimationFrame(() => {
        stateQueued = false;
        const r = selRange() || (lastRange && inEditor(lastRange.startContainer) ? lastRange : null);
        const at = r ? r.startContainer : null;
        const on = (sel) => !!(at && closestIn(at, sel));
        const press = (cmd, v) => btn[cmd].setAttribute('aria-pressed', v ? 'true' : 'false');
        press('h3', on('h3')); press('h4', on('h4'));
        press('bold', on('strong')); press('italic', on('em')); press('underline', on('u')); press('strike', on('s'));
        press('code', on('code')); press('pre', on('pre')); press('quote', on('blockquote')); press('link', on('a'));
        const list = at && closestIn(at, 'ul, ol');
        press('ul', !!list && list.nodeName === 'UL'); press('ol', !!list && list.nodeName === 'OL');
        btn.indent.disabled = !list; btn.outdent.disabled = !list;
        btn.undo.disabled = hpos <= 0; btn.redo.disabled = hpos >= history.length - 1;
        const inTable = on('table'); const inPanel = on('.desc-panel');
        Object.values(btn).forEach((b) => { if (b.dataset.ctx) b.hidden = b.dataset.ctx === 'table' ? !inTable : !inPanel; });
        // merged cells: the row / column actions don't handle spans, so they are off there (DR-18)
        const spanned = inTable && !!closestIn(at, 'table').querySelector('[colspan], [rowspan]');
        ['rowAdd', 'rowDel', 'colAdd', 'colDel'].forEach((c) => { btn[c].disabled = spanned; btn[c].title = spanned ? t('editor.spannedTable') : ''; });
        // DR-19: pending inline marks (collapsed caret) show as pressed / unpressed
        pending.forEach((want, cmd) => btn[cmd].setAttribute('aria-pressed', want ? 'true' : 'false'));
        ctx.hidden = !inTable && !inPanel;
        // keep the roving tab stop on a usable button
        const stop = bar.querySelector('.rte-btn[tabindex="0"]');
        if (!stop || stop.hidden || stop.disabled) { bar.querySelectorAll('.rte-btn').forEach((b) => { b.tabIndex = -1; }); const first = visibleButtons()[0]; if (first) first.tabIndex = 0; }
      });
    }
    const visibleButtons = () => [...bar.querySelectorAll('.rte-btn')].filter((b) => !b.hidden && !b.disabled);

    /* commands */
    const exec = (name, arg) => document.execCommand(name, false, arg);
    function formatBlockToggle(tag) {
      const r = selRange();
      const cur = r && closestIn(r.startContainer, tag);
      if (tag === 'blockquote') {
        if (cur) { const first = cur.firstChild; unwrap(cur); if (first) placeCaret(first, false); return; }
        exec('formatBlock', '<blockquote>');
        return;
      }
      exec('formatBlock', cur ? '<p>' : `<${tag}>`);
    }
    function toggleInlineCode() {
      const r = selRange();
      if (!r) return;
      const cur = closestIn(r.startContainer, 'code');
      if (cur) { const first = cur.firstChild; const last = cur.lastChild; unwrap(cur); if (first && last) { const rr = document.createRange(); rr.setStartBefore(first); rr.setEndAfter(last); const s = window.getSelection(); s.removeAllRanges(); s.addRange(rr); } return; }
      if (r.collapsed) return;
      const code = el('code');
      code.appendChild(r.extractContents());
      code.querySelectorAll('strong, em, u, s, code, a').forEach(unwrap);
      r.insertNode(code);
      const rr = document.createRange(); rr.selectNodeContents(code);
      const s = window.getSelection(); s.removeAllRanges(); s.addRange(rr);
    }
    /** Inserts a block after the current top-level block (panels and tables go to the top level). */
    function insertBlock(node, intoFirst) {
      const r = selRange() || restoreRange();
      const topOnly = isPanel(node);
      const { block, container } = blockOf(r.startContainer);
      let ref = topOnly ? topBlockOf(r.startContainer) : block;
      const parent = topOnly ? content : container;
      if (ref && isEl(ref, 'P') && !hasText(ref) && ref.parentNode === parent) { parent.replaceChild(node, ref); }
      else if (ref) ref.after(node);
      else parent.appendChild(node);
      if (!node.nextSibling && !isPanel(node)) { const p = el('p'); p.appendChild(el('br')); node.after(p); }
      if (isPanel(node) && !node.nextSibling) { const p = el('p'); p.appendChild(el('br')); node.after(p); }
      placeCaret(intoFirst || node, false);
    }
    function insertTable() {
      const table = el('table'); const tb = el('tbody');
      for (let i = 0; i < 2; i++) { const tr = el('tr'); for (let j = 0; j < 2; j++) { const td = el('td'); td.appendChild(el('br')); tr.appendChild(td); } tb.appendChild(tr); }
      table.appendChild(tb);
      insertBlock(table, tb.querySelector('td'));
    }
    function insertPanel(variant) {
      const panel = el('div', 'desc-panel'); panel.dataset.variant = variant;
      const head = el('div', 'desc-panel-head'); head.textContent = t(variant === 'warning' ? 'editor.panelWarningHead' : 'editor.panelInfoHead');
      const body = el('div', 'desc-panel-body'); const p = el('p'); p.appendChild(el('br')); body.appendChild(p);
      panel.append(head, body);
      insertBlock(panel, p);
    }
    function tableOp(op) {
      const r = selRange(); if (!r) return;
      const cell = closestIn(r.startContainer, 'td, th'); if (!cell) return;
      const tr = cell.parentNode; const table = cell.closest('table');
      const col = [...tr.children].indexOf(cell);
      const rows = [...table.querySelectorAll('tr')];
      if (op === 'rowAdd') { const nr = el('tr'); [...tr.children].forEach(() => { const td = el('td'); td.appendChild(el('br')); nr.appendChild(td); }); (tr.parentNode.nodeName === 'THEAD' ? (table.querySelector('tbody') || table.appendChild(el('tbody'))).prepend(nr) : tr.after(nr)); placeCaret(nr.children[col] || nr.firstChild, false); }
      if (op === 'rowDel') { const next = tr.nextElementSibling || tr.previousElementSibling; tr.remove(); if (!table.querySelector('tr')) { const p = el('p'); p.appendChild(el('br')); table.replaceWith(p); placeCaret(p, false); } else if (next) placeCaret(next.children[Math.min(col, next.children.length - 1)], false); }
      if (op === 'colAdd') { rows.forEach((row) => { const ref = row.children[col]; const c = el(ref && ref.nodeName === 'TH' ? 'th' : 'td'); c.appendChild(el('br')); if (ref) ref.after(c); else row.appendChild(c); }); placeCaret(tr.children[col + 1], false); }
      if (op === 'colDel') { rows.forEach((row) => { if (row.children[col]) row.children[col].remove(); }); if (!table.querySelector('td, th')) { const p = el('p'); p.appendChild(el('br')); table.replaceWith(p); placeCaret(p, false); } else placeCaret(tr.children[Math.max(0, col - 1)] || table.querySelector('td, th'), false); }
    }
    function removePanel() {
      const r = selRange(); if (!r) return;
      const panel = closestIn(r.startContainer, '.desc-panel'); if (!panel) return;
      const head = [...panel.children].find(isPanelHead); const body = [...panel.children].find(isPanelBody);
      const nodes = [];
      if (head && hasText(head)) { const p = el('p'); p.append(...head.childNodes); nodes.push(p); }
      if (body) nodes.push(...body.childNodes);
      if (!nodes.length) { const p = el('p'); p.appendChild(el('br')); nodes.push(p); }
      panel.replaceWith(...nodes);
      placeCaret(nodes[0], false);
    }
    function clearFormatting() {
      exec('removeFormat');
      exec('unlink');
      const r = selRange();
      if (r) content.querySelectorAll('code, strong, em, u, s').forEach((x) => { if (r.intersectsNode(x)) unwrap(x); });
    }

    function run(cmd) {
      if (cmd === 'undo') { undo(); content.focus({ preventScroll: true }); return; }
      if (cmd === 'redo') { redo(); content.focus({ preventScroll: true }); return; }
      if (cmd === 'link') { openLinkForm(); return; }
      content.focus({ preventScroll: true });
      restoreRange();
      switch (cmd) {
        case 'h3': case 'h4': formatBlockToggle(cmd); break;
        case 'pre': formatBlockToggle('pre'); break;
        case 'quote': formatBlockToggle('blockquote'); break;
        case 'bold': case 'italic': case 'underline': case 'strike': case 'code': {
          const rr = selRange();
          if (rr && rr.collapsed) { togglePending(cmd); return; } // nothing selected: applies to the next typed text
          if (cmd === 'code') toggleInlineCode(); else exec(cmd === 'strike' ? 'strikeThrough' : cmd);
          break;
        }
        case 'ul': exec('insertUnorderedList'); break;
        case 'ol': exec('insertOrderedList'); break;
        case 'indent': {
          const li = closestIn(selRange() && selRange().startContainer, 'li');
          let depth = 0;
          for (let x = li; x && x !== content; x = x.parentNode) if (x.nodeName === 'UL' || x.nodeName === 'OL') depth++;
          if (li && depth < MAX_LIST_DEPTH) exec('indent'); // deeper lists are refused (CR-18)
          break;
        }
        case 'outdent': if (closestIn(selRange() && selRange().startContainer, 'li')) exec('outdent'); break;
        case 'hr': { const hr = el('hr'); insertBlock(hr, null); const after = hr.nextElementSibling; if (after) placeCaret(after, false); break; }
        case 'table': insertTable(); break;
        case 'panelInfo': insertPanel('info'); break;
        case 'panelWarning': insertPanel('warning'); break;
        case 'clear': clearFormatting(); break;
        case 'rowAdd': case 'rowDel': case 'colAdd': case 'colDel': tableOp(cmd); break;
        case 'panelRemove': removePanel(); break;
        default: return;
      }
      normalizeLive();
      const r = selRange(); if (r) lastRange = r.cloneRange();
      commit('command');
    }

    /* link form */
    let linkTarget = null; // existing <a> being edited
    let linkRange = null;
    function openLinkForm() {
      content.focus({ preventScroll: true });
      const r = restoreRange();
      linkRange = r.cloneRange();
      linkTarget = closestIn(r.startContainer, 'a');
      lfTextIn.value = linkTarget ? linkTarget.textContent : r.toString();
      lfUrlIn.value = linkTarget ? linkTarget.getAttribute('data-href') || '' : '';
      lfRemove.hidden = !linkTarget;
      lfErr.hidden = true; lfUrlIn.removeAttribute('aria-invalid');
      linkForm.hidden = false;
      (lfUrlIn.value ? lfTextIn : lfUrlIn).focus();
    }
    function closeLinkForm(refocus) {
      linkForm.hidden = true;
      if (refocus) { content.focus({ preventScroll: true }); if (linkRange) { const s = window.getSelection(); s.removeAllRanges(); s.addRange(linkRange); } }
    }
    function applyLink() {
      const url = lfUrlIn.value.trim();
      if (!HTTP.test(url)) { lfErr.textContent = t('editor.linkInvalid'); lfErr.hidden = false; lfUrlIn.setAttribute('aria-invalid', 'true'); lfUrlIn.focus(); return; }
      const text = lfTextIn.value.replace(/\s+/g, ' ').trim() || url;
      closeLinkForm(true);
      if (linkTarget && linkTarget.isConnected) {
        linkTarget.setAttribute('data-href', url);
        if (text !== linkTarget.textContent) linkTarget.textContent = text;
        placeCaret(linkTarget, true);
      } else {
        const r = linkRange && inEditor(linkRange.startContainer) ? linkRange : restoreRange();
        const a = el('a'); a.setAttribute('data-href', url);
        if (!r.collapsed && r.toString() === text) a.appendChild(r.extractContents()); else { r.deleteContents(); a.textContent = text; }
        a.querySelectorAll('a').forEach(unwrap);
        r.insertNode(a);
        placeCaret(a, true);
      }
      normalizeLive();
      commit('command');
    }
    function removeLink() {
      closeLinkForm(true);
      if (linkTarget && linkTarget.isConnected) { const first = linkTarget.firstChild; unwrap(linkTarget); if (first) placeCaret(first, true); }
      normalizeLive();
      commit('command');
    }
    lfApply.addEventListener('click', applyLink);
    lfRemove.addEventListener('click', removeLink);
    lfCancel.addEventListener('click', () => closeLinkForm(true));
    linkForm.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeLinkForm(true); }
      if (e.key === 'Enter' && e.target.tagName === 'INPUT') { e.preventDefault(); applyLink(); }
    });

    /* paste / drop: only through the sanitizer */
    function textToFragment(text) {
      const frag = document.createDocumentFragment();
      const lines = String(text).replace(/\r\n?/g, '\n').replace(C0, '').split('\n');
      if (lines.length === 1) { frag.appendChild(document.createTextNode(lines[0])); return frag; }
      lines.forEach((line) => { const p = el('p'); if (line) p.textContent = line; else p.appendChild(el('br')); frag.appendChild(p); });
      return frag;
    }
    function htmlToFragment(htmlText) {
      const res = opts.sanitize(htmlText);
      const holder = el('div');
      holder.append(...res.root.childNodes);
      // Images can't be pasted: Jira only accepts images that are already in the issue's description.
      const imgs = holder.querySelectorAll('.ed-img');
      if (imgs.length) { imgs.forEach((x) => x.remove()); if (opts.onNotice) opts.onNotice(t('editor.pasteImage')); }
      normalize(holder, t);
      // a single pasted paragraph goes in inline
      if (holder.childNodes.length === 1 && isEl(holder.firstChild, 'P')) { const p = holder.firstChild; if (p.lastChild && isEl(p.lastChild, 'BR') && p.childNodes.length === 1) p.removeChild(p.lastChild); holder.replaceChildren(...p.childNodes); }
      const frag = document.createDocumentFragment();
      frag.append(...holder.childNodes);
      return frag;
    }
    function inlineOnly(frag) {
      const out = document.createDocumentFragment();
      [...frag.childNodes].forEach((n, i) => {
        if (isInlineNode(n)) { out.appendChild(n); return; }
        if (i && out.lastChild && !isEl(out.lastChild, 'BR')) out.appendChild(el('br'));
        if (isEl(n, 'PRE') || isEl(n, 'TABLE')) { out.appendChild(document.createTextNode(n.textContent)); return; }
        const holder = el('div'); holder.appendChild(n); flattenBlocks(holder, false);
        out.append(...holder.childNodes);
      });
      return out;
    }
    function insertFragment(frag, range) {
      const r = range || selRange() || restoreRange();
      r.deleteContents();
      const nodes = [...frag.childNodes];
      if (!nodes.length) return;
      const inlineCtx = closestIn(r.startContainer, 'li, td, th, pre, .desc-panel-head');
      if (nodes.every(isInlineNode) || inlineCtx) {
        const inl = closestIn(r.startContainer, 'pre') ? document.createTextNode(frag.textContent) : inlineOnly(frag);
        const last = inl.nodeType === 11 ? inl.lastChild : inl;
        r.insertNode(inl);
        if (last) { const rr = document.createRange(); rr.setStartAfter(last); rr.collapse(true); const s = window.getSelection(); s.removeAllRanges(); s.addRange(rr); }
        return;
      }
      const { block } = blockOf(r.startContainer);
      if (!block) { nodes.forEach((n) => content.appendChild(n)); placeCaret(nodes[nodes.length - 1], true); return; }
      const tail = document.createRange();
      tail.setStart(r.startContainer, r.startOffset);
      tail.setEnd(block, block.childNodes.length);
      const after = block.cloneNode(false);
      after.appendChild(tail.extractContents());
      let ref = block;
      nodes.forEach((n) => { ref.after(n); ref = n; });
      if (hasText(after)) ref.after(after);
      if (!hasText(block)) block.remove();
      placeCaret(ref, true);
    }
    content.addEventListener('paste', (e) => {
      e.preventDefault();
      const dt = e.clipboardData;
      if (!dt) return;
      let htmlText = dt.getData('text/html');
      const text = dt.getData('text/plain');
      if (htmlText.length > MAX_PASTE_HTML) htmlText = ''; // too big to parse: plain text instead
      const hasImage = [...(dt.items || [])].some((i) => i.kind === 'file' && /^image\//.test(i.type));
      if (hasImage && opts.onNotice) opts.onNotice(t('editor.pasteImage'));
      if (!htmlText && !text) return;
      insertFragment(htmlText ? htmlToFragment(htmlText) : textToFragment(text));
      normalizeLive();
      commit('paste');
    });
    content.addEventListener('drop', (e) => {
      e.preventDefault();
      const dt = e.dataTransfer;
      if (!dt) return;
      let htmlText = dt.getData('text/html'); const text = dt.getData('text/plain');
      if (htmlText.length > MAX_PASTE_HTML) htmlText = '';
      if (!htmlText && !text) return;
      const pos = document.caretRangeFromPoint ? document.caretRangeFromPoint(e.clientX, e.clientY) : null;
      insertFragment(htmlText ? htmlToFragment(htmlText) : textToFragment(text), pos && inEditor(pos.startContainer) ? pos : null);
      normalizeLive();
      commit('paste');
    });
    content.addEventListener('beforeinput', (e) => {
      // Clipboard / drag content only ever enters through the handlers above.
      if (/^insertFrom(Paste|Drop|Yank|PasteAsQuotation)$/.test(e.inputType) || (e.inputType === 'insertReplacementText' && e.dataTransfer && e.dataTransfer.types.includes('text/html'))) { e.preventDefault(); return; }
      if (e.inputType === 'insertText' && pending.size && e.data) { e.preventDefault(); if (insertWithMarks(e.data)) { normalizeLive(); commit('typing'); } return; }
      if (e.inputType === 'historyUndo') { e.preventDefault(); undo(); }
      if (e.inputType === 'historyRedo') { e.preventDefault(); redo(); }
    });
    content.addEventListener('input', (e) => {
      normalizeLive();
      commit(/^(insertText|deleteContent)/.test(e.inputType || '') ? 'typing' : 'command');
    });
    content.addEventListener('keydown', (e) => {
      const mod = e.ctrlKey || e.metaKey;
      const k = e.key.toLowerCase();
      if (mod && !e.altKey) {
        const map = { b: 'bold', i: 'italic', u: 'underline', k: 'link' };
        if (e.code === 'KeyZ' || k === 'z') { e.preventDefault(); if (e.shiftKey) redo(); else undo(); return; }
        if (e.code === 'KeyY' || k === 'y') { e.preventDefault(); redo(); return; }
        const c = map[k] || { KeyB: 'bold', KeyI: 'italic', KeyU: 'underline', KeyK: 'link' }[e.code];
        if (c) { e.preventDefault(); run(c); return; }
      }
      if (e.key === 'Tab' && !mod && !e.altKey) {
        const r = selRange();
        if (r && closestIn(r.startContainer, 'li')) { e.preventDefault(); run(e.shiftKey ? 'outdent' : 'indent'); }
        return; // outside lists Tab moves focus as usual
      }
      if (e.key === 'Enter' && !e.shiftKey) {
        const r = selRange();
        const head = r && closestIn(r.startContainer, '.desc-panel-head');
        if (head) { e.preventDefault(); const body = head.nextElementSibling; if (body) placeCaret(body.firstElementChild || body, false); }
      }
    });
    const onSelection = () => {
      const r = selRange();
      if (!r) return;
      lastRange = r.cloneRange();
      if (pending.size && (!pendingAt || pendingAt[0] !== r.startContainer || pendingAt[1] !== r.startOffset || !r.collapsed)) { pending.clear(); pendingAt = null; }
      updateState();
    };
    document.addEventListener('selectionchange', onSelection);

    /* toolbar: roving tabindex; buttons don't steal the selection on mouse down */
    bar.addEventListener('mousedown', (e) => { if (e.target.closest('.rte-btn')) e.preventDefault(); });
    bar.addEventListener('click', (e) => { const b = e.target.closest('.rte-btn'); if (b && !b.disabled) run(b.dataset.cmd); });
    bar.addEventListener('keydown', (e) => {
      const b = e.target.closest('.rte-btn');
      if (!b) return;
      const list = visibleButtons();
      const i = list.indexOf(b);
      const go = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: list.length - 1 }[e.key];
      if (go == null) return;
      e.preventDefault();
      const target = list[(go + list.length) % list.length];
      list.forEach((x) => { x.tabIndex = -1; });
      target.tabIndex = 0;
      target.focus();
    });

    function load(htmlText) {
      const holder = el('div');
      let lossy = false;
      if (htmlText && String(htmlText).trim()) {
        const res = opts.sanitize(String(htmlText));
        lossy = !!res.lossy;
        holder.append(...res.root.childNodes);
      }
      normalize(holder, t);
      content.replaceChildren(...holder.childNodes);
      history = [];
      hpos = -1;
      lastKind = '';
      loading = true;
      commit('load');
      loading = false;
      baseline = serialize(content);
      return { lossy };
    }

    return {
      el: content,
      load,
      getHTML: () => serialize(content),
      /** True when the serialized document differs from what was loaded. */
      isChanged: () => serialize(content) !== baseline,
      isEmpty: () => isEmptyDoc(content),
      focus: () => { content.focus(); },
      destroy: () => { document.removeEventListener('selectionchange', onSelection); },
      setInvalid: (on, describedBy) => {
        if (on) content.setAttribute('aria-invalid', 'true'); else content.removeAttribute('aria-invalid');
        content.setAttribute('aria-describedby', describedBy || '');
      },
    };
  }

  window.JiraffeEditor = { create, normalize, serialize, isEmptyDoc, isHttpUrl, dimension };
})();
