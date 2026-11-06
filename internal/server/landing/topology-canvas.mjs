// Optional presentation only. The ordinary HTML owns content and selection.
const PROVIDERS = new Set(['aws', 'gcp', 'azure']);
const PAINT_DEADLINE_MS = 2000;
const MAX_PIXEL_WIDTH = 2048;
const MAX_PIXEL_HEIGHT = 1024;

function drawableBelongsTo(canvas, element) {
  return element?.ownerDocument === canvas.ownerDocument &&
    element.closest?.('canvas') === canvas && element.hasAttribute('drawable') &&
    Boolean(element.closest('[inert]')) && !element.id &&
    !element.matches('input, textarea, select, button, iframe, [contenteditable]') &&
    !element.querySelector('input, textarea, select, button, iframe, [contenteditable], a[href]');
}

function readScene(canvas, scene) {
  if (!scene || !Array.isArray(scene.nodes) || !scene.nodes.length || scene.nodes.length > 9 ||
      !Array.isArray(scene.edges) || scene.edges.length > 12 || !PROVIDERS.has(scene.provider)) {
    return null;
  }
  const nodes = new Map();
  for (const node of scene.nodes) {
    if (!node || typeof node.id !== 'string' || !node.id || node.id.length > 64 ||
        nodes.has(node.id) || !PROVIDERS.has(node.provider) ||
        !Number.isFinite(node.x) || !Number.isFinite(node.y) ||
        node.x < 0 || node.x > 1 || node.y < 0 || node.y > 1 ||
        !drawableBelongsTo(canvas, node.element)) return null;
    nodes.set(node.id, Object.freeze({
      id: node.id, provider: node.provider, element: node.element, x: node.x, y: node.y,
    }));
  }
  if (nodes.get(scene.selectedId)?.provider !== scene.provider) return null;
  const edges = [];
  for (const edge of scene.edges) {
    if (!edge || edge.from === edge.to || !PROVIDERS.has(edge.provider) ||
        nodes.get(edge.from)?.provider !== edge.provider ||
        nodes.get(edge.to)?.provider !== edge.provider) return null;
    edges.push(Object.freeze({from: edge.from, to: edge.to, provider: edge.provider}));
  }
  return {nodes, edges, provider: scene.provider, selectedId: scene.selectedId};
}

export function mountTopologyEnhancement({root, canvas, scene} = {}) {
  let alive = true;
  let observer = null;
  let context = null;
  let deadline = null;
  let requested = false;
  let viewport = null;
  const subscriptions = [];
  const view = canvas?.ownerDocument?.defaultView;

  function destroy() {
    if (!alive) return;
    alive = false;
    if (deadline !== null) view?.clearTimeout(deadline);
    observer?.disconnect();
    for (const [target, event, listener] of subscriptions) target.removeEventListener(event, listener);
    if (canvas) canvas.hidden = true;
  }
  const handle = Object.freeze({destroy});
  function listen(target, event, listener) {
    target.addEventListener(event, listener);
    subscriptions.push([target, event, listener]);
  }

  try {
    if (!root || !canvas || !view || !root.contains(canvas) ||
        canvas.getAttribute('aria-hidden') !== 'true') {
      destroy();
      return handle;
    }
    canvas.hidden = true;
    const graph = readScene(canvas, scene);
    // Current Chrome API only: no legacy flags, guessed API, or polyfill.
    if (!graph || canvas.content !== 'drawable' || typeof canvas.requestPaint !== 'function') {
      destroy();
      return handle;
    }
    context = canvas.getContext('2d');
    if (!context || typeof context.drawElementImage !== 'function') {
      destroy();
      return handle;
    }
    let provider = graph.provider;
    let selectedId = graph.selectedId;

    function measure(entry) {
      const box = canvas.getBoundingClientRect();
      if (!Number.isFinite(box.width) || !Number.isFinite(box.height) ||
          box.width <= 0 || box.height <= 0) return false;
      const dpr = Math.min(2, Math.max(1, view.devicePixelRatio || 1));
      const deviceBox = entry?.devicePixelContentBoxSize;
      const physical = Array.isArray(deviceBox) ? deviceBox[0] : deviceBox;
      const width = Math.min(MAX_PIXEL_WIDTH, Math.max(1,
        Math.round(physical?.inlineSize || box.width * dpr)));
      const height = Math.min(MAX_PIXEL_HEIGHT, Math.max(1,
        Math.round(physical?.blockSize || box.height * dpr)));
      if (canvas.width !== width) canvas.width = width;
      if (canvas.height !== height) canvas.height = height;
      viewport = {width: box.width, height: box.height, pixelsX: width, pixelsY: height};
      return true;
    }

    function request(entry) {
      if (!alive) return;
      try {
        if (!measure(entry)) {
          destroy();
          return;
        }
        if (requested) return;
        requested = true;
        // A missing native paint cannot leave stale decorative selection visible.
        deadline = view.setTimeout(destroy, PAINT_DEADLINE_MS);
        canvas.requestPaint();
      } catch {
        destroy();
      }
    }

    function paint() {
      if (!alive || !viewport) return;
      requested = false;
      if (deadline !== null) view.clearTimeout(deadline);
      deadline = null;
      try {
        const {width, height, pixelsX, pixelsY} = viewport;
        context.setTransform(1, 0, 0, 1, 0, 0);
        context.clearRect(0, 0, pixelsX, pixelsY);
        context.setTransform(pixelsX / width, 0, 0, pixelsY / height, 0, 0);
        const cardWidth = Math.min(176, Math.max(64, width * 0.23));
        const cardHeight = Math.min(72, cardWidth * 0.44);
        const positions = new Map();
        for (const node of graph.nodes.values()) {
          if (node.provider !== provider) continue;
          if (!drawableBelongsTo(canvas, node.element)) throw new Error('Drawable retired');
          positions.set(node.id, {
            x: Math.max(cardWidth / 2, Math.min(width - cardWidth / 2, node.x * width)),
            y: Math.max(cardHeight / 2, Math.min(height - cardHeight / 2, node.y * height)),
          });
        }
        context.lineWidth = 2;
        context.strokeStyle = '#8098ad';
        for (const edge of graph.edges) {
          if (edge.provider !== provider) continue;
          const from = positions.get(edge.from);
          const to = positions.get(edge.to);
          const dx = to.x - from.x;
          const dy = to.y - from.y;
          const clip = Math.min(0.45, 1 / Math.max(
            Math.abs(dx) / (cardWidth / 2 + 7), Math.abs(dy) / (cardHeight / 2 + 7),
          ));
          const start = {x: from.x + dx * clip, y: from.y + dy * clip};
          const end = {x: to.x - dx * clip, y: to.y - dy * clip};
          const angle = Math.atan2(dy, dx);
          context.beginPath();
          context.moveTo(start.x, start.y);
          context.lineTo(end.x, end.y);
          context.lineTo(end.x - 7 * Math.cos(angle - Math.PI / 6),
            end.y - 7 * Math.sin(angle - Math.PI / 6));
          context.moveTo(end.x, end.y);
          context.lineTo(end.x - 7 * Math.cos(angle + Math.PI / 6),
            end.y - 7 * Math.sin(angle + Math.PI / 6));
          context.stroke();
        }
        for (const node of graph.nodes.values()) {
          if (node.provider !== provider) continue;
          const {x, y} = positions.get(node.id);
          if (node.id === selectedId) {
            context.fillStyle = '#d5f2ed';
            context.fillRect(x - cardWidth / 2 - 5, y - cardHeight / 2 - 5,
              cardWidth + 10, cardHeight + 10);
          }
          // Only native paint snapshots of our inert presentation descendants.
          context.drawElementImage(node.element, x - cardWidth / 2, y - cardHeight / 2,
            cardWidth, cardHeight);
        }
      } catch {
        destroy();
      }
    }

    function selection(event) {
      const detail = event.detail;
      if (!alive || !detail || !PROVIDERS.has(detail.provider) ||
          graph.nodes.get(detail.selectedId)?.provider !== detail.provider ||
          (provider === detail.provider && selectedId === detail.selectedId)) return;
      provider = detail.provider;
      selectedId = detail.selectedId;
      request();
    }

    listen(canvas, 'paint', paint);
    listen(root, 'topology:change', selection);
    if (typeof view.ResizeObserver === 'function') {
      observer = new view.ResizeObserver(entries => {
        const entry = entries.find(item => item.target === canvas);
        if (entry) request(entry);
      });
      try { observer.observe(canvas, {box: 'device-pixel-content-box'}); }
      catch { observer.observe(canvas); }
    } else {
      listen(view, 'resize', () => request());
    }
    // Rendering must be possible before the first snapshot. This reveals only
    // optional decoration; unsupported/error paths never hide ordinary HTML.
    canvas.hidden = false;
    request();
    // No animation or idle frame loop, including with reduced-motion enabled.
  } catch {
    destroy();
  }
  return handle;
}
