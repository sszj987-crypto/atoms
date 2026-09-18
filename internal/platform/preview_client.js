(() => {
  const { prefix, origin } = __ATOMS_PREVIEW_CONFIG__;
  if (window.top === window.self) { location.replace(origin); return; }
  const nativeFetch = window.fetch.bind(window);
  const scoped = value => {
    const url = new URL(value, location.href);
    if (url.origin !== origin) return null;
    if (url.pathname !== prefix && !url.pathname.startsWith(prefix + '/')) url.pathname = prefix + url.pathname;
    return url.href;
  };
  let sequence = 0;
  const pending = new Map();
  window.addEventListener('message', event => {
    if (event.source !== window.parent || event.origin !== origin || event.data?.type !== 'atoms-preview-response') return;
    const entry = pending.get(event.data.id);
    if (!entry) return;
    pending.delete(event.data.id); clearTimeout(entry.timer);
    if (event.data.error) entry.reject(new TypeError(event.data.error));
    else {
      const response = new Response(event.data.status === 204 || event.data.status === 304 ? null : event.data.body,
        { status: event.data.status, statusText: event.data.statusText, headers: event.data.headers });
      Object.defineProperty(response, 'url', {value:event.data.url});
      entry.resolve(response);
    }
  });
  window.fetch = async (input, init) => {
    const request = new Request(input, init);
    const url = scoped(request.url);
    if (!url) return nativeFetch(request);
    if (request.signal.aborted) throw new DOMException('Aborted', 'AbortError');
    const id = ++sequence;
    const body = ['GET', 'HEAD'].includes(request.method) ? null : await request.arrayBuffer();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { pending.delete(id); reject(new TypeError('Preview request timed out')); }, 150000);
      pending.set(id, {resolve, reject, timer});
      request.signal.addEventListener('abort', () => {
        pending.delete(id); clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError'));
        window.parent.postMessage({type:'atoms-preview-abort', id}, origin);
      }, {once:true});
      window.parent.postMessage({type:'atoms-preview-request', id, url, method:request.method, headers:[...request.headers], body}, origin);
    });
  };
  const nativeOpen = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url, ...rest) { return nativeOpen.call(this, method, scoped(url) || url, ...rest); };
  const NativeWebSocket = window.WebSocket;
  window.WebSocket = class extends NativeWebSocket {
    constructor(value, protocols) {
      const url = new URL(value, location.href);
      const httpURL = new URL(url.href); httpURL.protocol = httpURL.protocol === 'wss:' ? 'https:' : 'http:';
      const mapped = scoped(httpURL.href);
      if (mapped) { const adjusted = new URL(mapped); adjusted.protocol = url.protocol; value = adjusted.href; }
      super(value, protocols);
    }
  };
  const fixURL = (el, attr) => {
    const value = el.getAttribute(attr);
    if (value?.startsWith('/') && !value.startsWith('//') && value !== prefix && !value.startsWith(prefix+'/')) el.setAttribute(attr, prefix+value);
  };
  const fixElement = el => { if (el instanceof Element) for (const attr of ['src', 'href', 'action', 'poster']) fixURL(el, attr); };
  new MutationObserver(records => { for (const record of records) {
    fixElement(record.target);
    for (const node of record.addedNodes) if (node instanceof Element) { fixElement(node); node.querySelectorAll('[src],[href],[action],[poster]').forEach(fixElement); }
  }}).observe(document.documentElement, {subtree:true, childList:true, attributes:true, attributeFilter:['src','href','action','poster']});
  document.addEventListener('click', event => { const link = event.target.closest?.('a[href]'); if (link) fixURL(link, 'href'); }, true);
  document.addEventListener('submit', event => fixURL(event.target, 'action'), true);
  // Opaque sandbox frames cannot use browser storage. Keep app storage in a
  // separate project namespace owned by the parent, never platform storage.
  let initial = {};
  if (location.hash.startsWith('#atoms-storage=')) {
    try { initial = JSON.parse(decodeURIComponent(location.hash.slice(15))); } catch {}
    try { history.replaceState(history.state, '', location.pathname+location.search); } catch {}
  }
  for (const kind of ['local', 'session']) {
    const data = new Map(Object.entries(initial[kind] || {}));
    const sync = () => window.parent.postMessage({type:'atoms-preview-storage', kind, values:Object.fromEntries(data)}, origin);
    const storage = { get length() {return data.size;}, key(index) {return [...data.keys()][index] ?? null;},
      getItem(key) {return data.get(String(key)) ?? null;}, setItem(key,value) {data.set(String(key),String(value));sync();},
      removeItem(key) {data.delete(String(key));sync();}, clear() {data.clear();sync();} };
    Object.defineProperty(window, kind+'Storage', {value:storage, configurable:false});
  }
})();
