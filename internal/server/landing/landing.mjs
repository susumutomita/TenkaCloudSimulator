// Original semantic HTML controller. Canvas is optional and grants no authority.
import {TOPOLOGY} from './topology-data.mjs';
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
    root.dispatchEvent(new CustomEvent('topology:change', {detail: Object.freeze({provider, selectedId})}));
  }
  for (const link of links) link.addEventListener('click', event => {event.preventDefault(); select(link.dataset.nodeId);});
  for (const control of controls) {
    control.disabled = false;
    control.addEventListener('click', () => {
      const item = TOPOLOGY.providers.find(item => item.id === control.dataset.provider);
      if (item) select(item.defaultNode);
    });
  }
  select(selectedId);
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
