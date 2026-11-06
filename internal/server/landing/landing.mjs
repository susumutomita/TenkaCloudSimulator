// Original semantic HTML controller. Canvas is optional and grants no authority.
import {TOPOLOGY} from './topology-data.mjs';
// Decorative SVG strokes stop at actual card borders, never their text centers.
function connectorPath(from, to, frame) {
  const valid = rect => rect && ['left', 'top', 'right', 'bottom'].every(key => Number.isFinite(rect[key]))
    && rect.right > rect.left && rect.bottom > rect.top;
  if (![from, to, frame].every(valid)) return '';
  const centerX = rect => (rect.left + rect.right) / 2;
  const centerY = rect => (rect.top + rect.bottom) / 2;
  let start, end;
  if (from.right < to.left) start = [from.right, centerY(from)], end = [to.left, centerY(to)];
  else if (to.right < from.left) start = [from.left, centerY(from)], end = [to.right, centerY(to)];
  else if (from.bottom < to.top) start = [centerX(from), from.bottom], end = [centerX(to), to.top];
  else if (to.bottom < from.top) start = [centerX(from), from.top], end = [centerX(to), to.bottom];
  else return ''; // Overlapping/touching cards have no safe exterior gap.
  const project = ([x, y]) => `${(x - frame.left) * 1000 / (frame.right - frame.left)} ${(y - frame.top) * 360 / (frame.bottom - frame.top)}`;
  return `M ${project(start)} L ${project(end)}`;
}
const root = document.querySelector('[data-cloud-topology]');
function start() {
  if (!root) return;
  const links = [...root.querySelectorAll('a[data-node-id]')];
  const details = [...root.querySelectorAll('[data-node-detail]')];
  const groups = [...root.querySelectorAll('[data-provider-group]')];
  const controls = [...root.querySelectorAll('button[data-provider]')];
  const status = root.querySelector('[data-selection-status]');
  const canvas = root.querySelector('canvas[data-topology-canvas]');
  const ids = new Set(TOPOLOGY.nodes.map(node => node.id));
  if (links.length !== ids.size || details.length !== ids.size
    || new Set(links.map(link => link.dataset.nodeId)).size !== ids.size
    || links.some(link => !ids.has(link.dataset.nodeId))
    || details.some(detail => !ids.has(detail.dataset.nodeDetail))) return;
  let provider = 'aws', selectedId = 'aws-lambda';
  function routeEdges() {
    const group = groups.find(item => !item.hidden && item.dataset.providerGroup === provider);
    const svg = group?.querySelector?.('svg.topology-lines');
    if (!svg) return;
    const paths = [...svg.querySelectorAll('path')];
    const edges = TOPOLOGY.edges.filter(edge => edge.provider === provider);
    const frame = svg.getBoundingClientRect();
    paths.forEach((path, index) => {
      const edge = edges[index];
      const from = links.find(link => link.dataset.nodeId === edge?.from);
      const to = links.find(link => link.dataset.nodeId === edge?.to);
      path.setAttribute('d', paths.length === edges.length && from && to
        ? connectorPath(from.getBoundingClientRect(), to.getBoundingClientRect(), frame) : '');
    });
  }
  function nodeFromFragment(hash) {
    if (!hash.startsWith('#detail-')) return null;
    const id = hash.slice('#detail-'.length);
    return ids.has(id) ? id : null;
  }
  function select(id) {
    const node = TOPOLOGY.nodes.find(item => item.id === id);
    if (!node) return;
    provider = node.provider; selectedId = node.id;
    for (const group of groups) group.hidden = group.dataset.providerGroup !== provider;
    for (const control of controls) control.setAttribute('aria-pressed', String(control.dataset.provider === provider));
    for (const link of links) {
      if (link.dataset.nodeId === selectedId) link.setAttribute('aria-current', 'true');
      else link.removeAttribute('aria-current');
    }
    for (const detail of details) detail.hidden = detail.dataset.nodeDetail !== selectedId;
    if (status) status.textContent = `${node.title} の役割と制限`;
    routeEdges();
    root.dispatchEvent(new CustomEvent('topology:change', {detail: Object.freeze({provider, selectedId})}));
  }
  for (const link of links) link.addEventListener('click', event => {
    if (event.defaultPrevented || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    // Reveal the target before native fragment navigation; leave URL, scroll,
    // keyboard activation and modified-link gestures to the browser.
    select(link.dataset.nodeId);
  });
  for (const control of controls) {
    control.disabled = false;
    control.addEventListener('click', () => {
      const item = TOPOLOGY.providers.find(item => item.id === control.dataset.provider);
      if (item) select(item.defaultNode);
    });
  }
  window.addEventListener('hashchange', () => {
    const id = nodeFromFragment(window.location.hash);
    if (id) select(id);
  });
  select(nodeFromFragment(window.location.hash) || selectedId);
  window.addEventListener('resize', routeEdges);
  document.fonts?.ready.then(routeEdges).catch(() => {});
  if (!canvas || !status) return;
  // Only this decorative projection is generated. The readable HTML above is
  // already present, and remains the authoritative keyboard/content interface.
  const nodes = TOPOLOGY.nodes.map(node => {
    const element = document.createElement('span');
    element.className = 'drawable-node';
    element.setAttribute('drawable', '');
    element.setAttribute('data-topology-drawable', '');
    element.setAttribute('aria-hidden', 'true');
    element.setAttribute('inert', '');
    element.textContent = `${node.title}\n${node.subtitle}`;
    element.style.left = `${node.x * 100}%`;
    element.style.top = `${node.y * 100}%`;
    canvas.append(element);
    return Object.freeze({id: node.id, provider: node.provider, element, x: node.x, y: node.y});
  });
  import('./topology-canvas.mjs').then(({mountTopologyEnhancement}) => {
    // Capture the latest ordinary selection after the optional module loads.
    const scene = Object.freeze({provider, selectedId, nodes: Object.freeze(nodes), edges: TOPOLOGY.edges});
    const handle = mountTopologyEnhancement({root, canvas, scene});
    window.addEventListener('pagehide', () => handle?.destroy?.(), {once: true});
  }).catch(() => {canvas.hidden = true;});
}
start();
