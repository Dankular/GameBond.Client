// Network topology views: a live map of the exit server, bonded links, this PC and devices behind it,
// plus a Docker-backed Lab designer. Both modes share one SVG canvas and node/link primitives.
// Clicking a device opens its card. Devices that route through the tunnel have measured usage
// (current_status.device_usage); other LAN devices are listed but their traffic is not visible.
const topoStyle = document.createElement('style');
topoStyle.textContent = '.subtabs{display:flex;gap:6px;margin:8px 0 12px}.subtabs .seg.active{background:#0f7ae5;color:#fff}'
  + '.topo-head{text-align:center;font-size:20px;margin:6px 0 10px}.topo-head b{font-weight:500}.topo-head .good{color:#2fb800}.topo-head .fair{color:#f5a300}.topo-head .poor{color:#e0201b}'
  + '.topo-stats{display:flex;gap:10px;flex-wrap:wrap;margin:10px 0}.topo-stat{flex:1;min-width:110px;border:1px solid #e1e5eb;border-radius:10px;padding:8px 10px;text-align:center}.topo-stat strong{display:block;font-size:22px;font-weight:500}.topo-stat span{color:#7b8089}'
  + '.topo-svg{width:100%;height:auto;border:1px solid #e1e5eb;border-radius:10px;background:#fafbfc}.topo-node{cursor:pointer}.topo-svg [data-live-node]{cursor:grab;touch-action:none}.topo-svg [data-live-node]:active{cursor:grabbing}.topo-node:focus{outline:none}.topo-node:hover rect,.topo-node:focus rect,.topo-node.sel rect{stroke:#0f7ae5;stroke-width:2}'
  + '.dev-card{border:1px solid #e1e5eb;border-radius:10px;padding:14px 16px;margin:14px 0}.dev-card h2{margin:0 0 2px;font-size:16px;display:flex;gap:10px;align-items:center}.dev-card .sub{color:#7b8089;margin:0 0 10px}'
  + '.chip{font-size:11px;border-radius:10px;padding:1px 8px;background:#e8edf3;color:#505762}.chip.good{background:#dff6d5;color:#2a7a00}.chip.poor{background:#fde0df;color:#a8120e}'
  + '.dev-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:14px}.dev-grid>div{min-width:0}.dev-grid h3{margin:0 0 6px;font-size:12px;text-transform:uppercase;color:#7b8089;font-weight:500}'
  + '.gauges{display:flex;gap:16px;align-items:center}.gauge{text-align:center}.gauge b{display:block;font-size:16px;font-weight:500}.gauge span{color:#7b8089}'
  + '.svc-row{display:grid;grid-template-columns:14px minmax(90px,1.2fr) 2fr 60px;gap:6px;align-items:center;margin:3px 0}.svc-row i{width:10px;height:10px;border-radius:50%;display:inline-block}.svc-bar{height:5px;border-radius:3px;background:#e8edf3;overflow:hidden}.svc-bar div{height:100%}'
  + '.range{display:flex;gap:4px;justify-content:flex-end;margin-bottom:4px}.range .seg.active{background:#0f7ae5;color:#fff}.notice{background:#f4f6f9;border-radius:8px;padding:10px 12px;color:#505762}';
topoStyle.textContent += '.tf{fill:none;stroke-linecap:round;stroke-dasharray:5 11;pointer-events:none;stroke-width:calc(2px + 2.5px*var(--i))}.tf.down{stroke:#2fb800;--c:47,184,0}.tf.up{stroke:#0f7ae5;--c:15,122,229}.tf-halo{fill:none;stroke-linecap:round;pointer-events:none;stroke-width:calc(4px + 10px*var(--i));opacity:calc(.08 + .22*var(--i));filter:blur(calc(1px + 3px*var(--i)))}.tf-halo.down{stroke:#2fb800}.tf-halo.up{stroke:#0f7ae5}@keyframes tf-fwd{from{stroke-dashoffset:16}to{stroke-dashoffset:0}}@keyframes tf-rev{from{stroke-dashoffset:0}to{stroke-dashoffset:16}}@keyframes tf-glow{0%,100%{opacity:calc(.5 + .25*var(--i));filter:drop-shadow(0 0 calc(1px + 3px*var(--i)) rgb(var(--c)))}50%{opacity:1;filter:drop-shadow(0 0 calc(3px + 9px*var(--i)) rgb(var(--c)))}}@keyframes tf-halo-pulse{0%,100%{opacity:calc(.05 + .15*var(--i))}50%{opacity:calc(.12 + .35*var(--i))}}@media (prefers-reduced-motion:reduce){.tf,.tf-halo{animation:none!important}}';
topoStyle.textContent += '.port-active{color:#00a66c;font-weight:600}.gl{stroke:#e8edf3;stroke-width:1}'
  + '.usage-tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin:4px 0 10px}.usage-tile{border:1px solid #e1e5eb;border-radius:10px;padding:8px 10px;text-align:center}.usage-tile b{display:block;font-size:18px;font-weight:600}.usage-tile span{color:#7b8089;font-size:11px}.usage-tile small{display:block;color:#7b8089;font-size:11px;text-transform:uppercase}'
  + '.topo-pies{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:12px;margin:14px 0}.pie-card{border:1px solid #e1e5eb;border-radius:10px;padding:10px 14px;min-width:0}.pie-card h3{margin:0 0 6px;font-size:12px;text-transform:uppercase;color:#7b8089}'
  + '.pie-body{display:flex;gap:14px;align-items:center}.pie-svg{width:128px;height:128px;flex:none}.pie-track{stroke:#e8edf3}.pie-num{fill:#393b43;font-weight:600}.pie-legend{flex:1;min-width:0;font-size:12px}'
  + '.pie-legend div{display:grid;grid-template-columns:12px minmax(0,1fr) auto;gap:6px;align-items:center;margin:2px 0}.pie-legend i{width:9px;height:9px;border-radius:2px;display:inline-block}.pie-legend span{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.pie-legend b{font-weight:500;color:#505762}'
  + '.topo-network-metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(280px,1fr));gap:10px;margin:12px 0}.topo-metric-card{border:1px solid #e1e5eb;border-radius:10px;padding:10px 12px;min-width:0}.topo-metric-card h3{margin:0 0 6px;font-size:12px;text-transform:uppercase;color:#7b8089}.topo-metric-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:6px}.topo-metric{border-radius:7px;background:#f4f6f9;padding:6px 8px;min-width:0}.topo-metric b{display:block;font-size:15px;font-weight:600;overflow:hidden;text-overflow:ellipsis}.topo-metric span{font-size:10px;color:#7b8089}.topo-network-row{border-top:1px solid #e8edf3;padding:7px 0;font-size:12px}.topo-network-row:first-of-type{border-top:0}.topo-network-row b{font-weight:600}.topo-network-row div{color:#7b8089;margin-top:2px}.topo-metric-note{font-size:10px;color:#7b8089;margin:6px 0 0}'
  + 'details.topo-metric-card{grid-column:1/-1}details.topo-metric-card summary{cursor:pointer;font-size:12px;text-transform:uppercase;color:#7b8089;font-weight:500}.topo-frame-log{max-height:280px;overflow:auto;font-size:11px;background:#f4f6f9;border-radius:7px;padding:8px;margin:8px 0 0;white-space:pre-wrap}'
  + '.pie-card .sub{color:#7b8089;font-size:11px;margin:6px 0 0}.tp-panel{margin:6px 0 10px}.tp-panel h4{margin:0 0 2px;font-size:12px;color:#505762;font-weight:600}.tp-legend{display:flex;gap:14px;flex-wrap:wrap;color:#7b8089;font-size:11px;margin:2px 0}.tp-legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:4px;vertical-align:-1px}';
topoStyle.textContent += '.tb-rect{fill:#fff;stroke:#cfd5dd}.tb-title{fill:#393b43}.tb-sub{fill:#7b8089}.tl{stroke:#aab2bd}.tl-label{fill:#505762}.g-bg{fill:#f4f6f9;stroke:#cfd5dd}.d-track{stroke:#e8edf3}.ax{stroke:#cfd5dd}';
document.head.append(topoStyle);

const SVGNS = 'http://www.w3.org/2000/svg';
function svg(tag, attributes = {}, text) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [key, value] of Object.entries(attributes)) el.setAttribute(key, value);
  if (text != null) el.textContent = text;
  return el;
}
function fmtBps(bps) {
  bps = Number(bps) || 0;
  if (bps >= 1e9) return (bps / 1e9).toFixed(2) + ' Gbps';
  if (bps >= 1e6) return (bps / 1e6).toFixed(bps >= 1e7 ? 1 : 2) + ' Mbps';
  if (bps >= 1e3) return (bps / 1e3).toFixed(1) + ' Kbps';
  return Math.round(bps) + ' bps';
}
function macKey(mac) { return String(mac || '').toLowerCase().replace(/[^0-9a-f]/g, ''); }
function ago(unix) {
  if (!unix) return '—';
  const s = Math.max(0, Date.now() / 1000 - unix);
  return s < 90 ? Math.round(s) + ' s ago' : s < 5400 ? Math.round(s / 60) + ' min ago' : s < 172800 ? (s / 3600).toFixed(1) + ' h ago' : Math.round(s / 86400) + ' d ago';
}

// Devices remains its own view. Live and Lab share one topology pane and canvas while
// keeping separate data sources and mode-specific controls.
const topoPane = element('div'), devicesPane = element('div');
devicesPane.append(...Array.from(scanPanel.children).slice(1));
const subTabs = element('div', null, { class: 'subtabs', role: 'tablist' });
const subDevices = element('button', 'Devices', { class: 'seg active', role: 'tab' }), subTopology = element('button', 'Topology', { class: 'seg', role: 'tab' });
subTabs.append(subDevices, subTopology);
scanPanel.append(subTabs, devicesPane, topoPane);
let scannerSub = 'devices', topologyMode = 'live';
function showScannerSub(which) {
  scannerSub = which;
  devicesPane.hidden = which !== 'devices'; topoPane.hidden = which !== 'topology';
  subDevices.classList.toggle('active', which === 'devices'); subTopology.classList.toggle('active', which === 'topology');
  subDevices.setAttribute('aria-selected', which === 'devices'); subTopology.setAttribute('aria-selected', which === 'topology');
  if (which === 'topology') renderTopology();
}
subDevices.onclick = () => showScannerSub('devices');
subTopology.onclick = () => showScannerSub('topology');
showScannerSub('devices');

const topoModeBar = element('div', null, { class: 'subtabs topo-mode-switch', role: 'group', 'aria-label': 'Topology view' });
const liveModeButton = element('button', 'Live', { class: 'seg active', 'aria-pressed': 'true' }), labModeButton = element('button', 'Lab', { class: 'seg', 'aria-pressed': 'false' });
topoModeBar.append(liveModeButton, labModeButton);
const topoHead = element('div', null, { class: 'topo-head', role: 'status' });
const topoStats = element('div', null, { class: 'topo-stats' });
const topoMap = svg('svg', { class: 'topo-svg', viewBox: '0 0 1060 360', role: 'group', 'aria-label': 'Network topology' });
const topoCard = element('div');
const topoNetworkMetrics = element('div', null, { class: 'topo-network-metrics' });
const topoPies = element('div', null, { class: 'topo-pies' });
const liveChrome = element('div'); liveChrome.append(topoNetworkMetrics, topoPies, topoCard);
topoPane.append(topoModeBar, topoHead, topoStats, topoMap, liveChrome);

// Lab authoring controls use the shared topology canvas and visual primitives. Desired state is
// saved by the server portal, never in the live status view.
const labStyle = document.createElement('style');
labStyle.textContent = '.lab-editor{margin:18px 0;padding:14px;border:1px solid #d9dfe7;border-radius:10px}.lab-toolbar{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin:8px 0}.lab-toolbar button,.lab-editor button{cursor:pointer}.lab-canvas{width:100%;height:auto;min-height:300px;border:1px solid #e1e5eb;border-radius:8px;background-color:#fbfcfe;background-image:radial-gradient(#dce2ea 1px,transparent 1px);background-size:18px 18px;touch-action:none}.lab-node{cursor:grab}.lab-node:active{cursor:grabbing}.lab-selected rect{stroke:#0f7ae5;stroke-width:3}.lab-props{display:flex;gap:8px;flex-wrap:wrap;align-items:center;margin-top:10px}.lab-props input,.lab-props select{padding:6px;border:1px solid #cfd5dd;border-radius:5px}.lab-msg{margin:8px 0;color:#505762;min-height:1.2em}.lab-msg.error{color:#a8120e}.lab-link{stroke:#687486;stroke-width:3;cursor:pointer}.lab-link.sel{stroke:#0f7ae5;stroke-width:5}.lab-help{font-size:12px;color:#7b8089}';
document.head.append(labStyle);
const labEditor = element('section', null, { class: 'lab-editor' });
labEditor.append(element('h2', 'Lab topology designer'));
const labToolbar = element('div', null, { class: 'lab-toolbar' });
const labNew = element('button', 'New topology'), labAddHost = element('button', 'Add host'), labAddRouter = element('button', 'Add router'), labAddSwitch = element('button', 'Add switch'), labLinkMode = element('button', 'Draw link'), labAddCheck = element('button', 'Add ping check'), labRunChecks = element('button', 'Run checks'), labSave = element('button', 'Save'), labLoad = element('button', 'Load saved'), labApply = element('button', 'Apply'), labRollback = element('button', 'Rollback');
const labName = document.createElement('input'); labName.placeholder = 'Topology name'; labName.value = 'New lab'; labName.maxLength = 128;
const labToolbarStatus = element('span', '', { class: 'lab-help' });
labToolbar.append(labName, labNew, labAddHost, labAddRouter, labAddSwitch, labLinkMode, labAddCheck, labRunChecks, labSave, labLoad, labApply, labRollback, labToolbarStatus);
const labCanvas = topoMap;
const labAssertions = element('div', null, { class: 'lab-toolbar' }), labProps = element('div', null, { class: 'lab-props' }), labMessage = element('div', null, { class: 'lab-msg', role: 'status' });
labEditor.append(labToolbar, labAssertions, labProps, labMessage, element('p', 'Choose Draw link, then select two nodes. Drag nodes to arrange them. Impairments apply in real time when the lab runtime is available.', { class: 'lab-help' }));
topoPane.append(labEditor);
function setTopologyMode(mode) {
  topologyMode = mode;
  const isLive = mode === 'live';
  liveModeButton.classList.toggle('active', isLive); labModeButton.classList.toggle('active', !isLive);
  liveModeButton.setAttribute('aria-pressed', String(isLive)); labModeButton.setAttribute('aria-pressed', String(!isLive));
  liveChrome.hidden = !isLive; labEditor.hidden = isLive;
  topoMap.setAttribute('aria-label', isLive ? 'Live network topology' : 'Editable lab topology');
  topoMap.classList.toggle('lab-canvas', !isLive);
  if (isLive) renderTopology(); else labRender();
}
liveModeButton.onclick = () => setTopologyMode('live');
labModeButton.onclick = () => setTopologyMode('lab');
let labTopology = { version: 1, id: 'new-lab', name: 'New lab', nodes: [], links: [] }, labSelected = null, labMode = 'select', labLinkStart = null, labDrag = null, labRuntimeStatus = null;
const labInterfaceFaults = new Map();
function labSay(message, error = false) { labMessage.textContent = message; labMessage.classList.toggle('error', error); }
function labFreshID(prefix, used) { let n = 1, id = prefix + '-' + n; while (used.has(id)) id = prefix + '-' + (++n); return id; }
function labAddNode(role) {
  const nodeID = labFreshID(role, new Set(labTopology.nodes.map(n => n.id)));
  const daemons = role === 'router' ? ['frr'] : role === 'switch' ? ['mstpd'] : [];
  const node = { id: nodeID, name: role[0].toUpperCase() + role.slice(1) + ' ' + (labTopology.nodes.length + 1), role, interfaces: [{ id: 'eth0', name: 'eth0', addresses: [] }], daemons, daemon_config: {}, position: { x: 100 + (labTopology.nodes.length % 4) * 230, y: 80 + Math.floor(labTopology.nodes.length / 4) * 130 } };
  labTopology.nodes.push(node); labSelected = { kind: 'node', id: node.id }; labMode = 'select'; labRender();
}
function labInterface(node) {
  const used = new Set(labTopology.links.flatMap(l => [l.a, l.b]).filter(e => e.node === node.id).map(e => e.interface));
  let i = 0, id; do { id = 'eth' + i++; } while (used.has(id));
  const found = node.interfaces.find(x => x.id === id);
  if (!found) node.interfaces.push({ id, name: id });
  return id;
}
function labRender() {
  if (topologyMode !== 'lab') return;
  labTopology.name = labName.value || labTopology.name;
  labCanvas.replaceChildren();
  labCanvas.setAttribute('viewBox', '0 0 1060 460');
  for (const link of labTopology.links) {
    const a = labTopology.nodes.find(n => n.id === link.a.node), b = labTopology.nodes.find(n => n.id === link.b.node); if (!a || !b) continue;
    const line = mapLine(a.position.x + 70, a.position.y + 32, b.position.x + 70, b.position.y + 32, false, null, null);
    line.classList.add('lab-link'); if (labSelected?.kind === 'link' && labSelected.id === link.id) line.classList.add('sel');
    const linkStats = labRuntimeStatus?.links?.[link.id] || {}, rate = Number(linkStats.bps ?? linkStats.tx_bps ?? linkStats.bytes_per_second * 8) || 0;
    const renderedLine = line.querySelector('line');
    renderedLine.style.strokeWidth = String(2 + 5 * flowIntensity(rate));
    if (linkStats.up === false) renderedLine.style.stroke = '#e0201b'; else if (rate > 0) renderedLine.style.stroke = '#0f7ae5';
    line.onclick = () => { labSelected = { kind: 'link', id: link.id }; labRender(); }; labCanvas.append(line);
    labCanvas.append(svg('text', { x: (a.position.x + b.position.x) / 2 + 60, y: (a.position.y + b.position.y) / 2 + 20, 'font-size': 11, 'text-anchor': 'middle', fill: '#505762' }, (link.impairment?.latency_ms || 0) + ' ms · ' + (link.impairment?.loss_pct || 0) + '% loss'));
  }
  for (const node of labTopology.nodes) {
    const g = nodeBox(node.position.x, node.position.y, 140, 64, node.name, node.role + ' · ' + node.interfaces.length + ' ports', 'idle', null, labSelected?.kind === 'node' && labSelected.id === node.id);
    g.classList.add('lab-node'); g.setAttribute('data-id', node.id);
    g.onpointerdown = e => { e.preventDefault(); if (labMode === 'link') { if (!labLinkStart) { labLinkStart = node.id; labSay('Select the second node to connect.'); } else if (labLinkStart !== node.id) { const other = labTopology.nodes.find(n => n.id === labLinkStart); const id = labFreshID('link', new Set(labTopology.links.map(x => x.id))); labTopology.links.push({ id, a: { node: other.id, interface: labInterface(other) }, b: { node: node.id, interface: labInterface(node) }, impairment: { latency_ms: 0, jitter_ms: 0, loss_pct: 0, bandwidth_bps: 0 } }); labSelected = { kind: 'link', id }; labLinkStart = null; labMode = 'select'; labSay('Link created.'); labRender(); } return; } const pt = labCanvas.createSVGPoint(); pt.x = e.clientX; pt.y = e.clientY; const p = pt.matrixTransform(labCanvas.getScreenCTM().inverse()); labDrag = { id: node.id, dx: p.x - node.position.x, dy: p.y - node.position.y }; labSelected = { kind: 'node', id: node.id }; labCanvas.setPointerCapture(e.pointerId); labRender(); };
    labCanvas.append(g);
  }
  labAssertions.replaceChildren();
  for (const assertion of (labTopology.assertions || [])) { const b = element('button', assertion.kind + ': ' + assertion.target); b.classList.toggle('active', labSelected?.kind === 'assertion' && labSelected.id === assertion.id); b.onclick = () => { labSelected = { kind: 'assertion', id: assertion.id }; labRender(); }; labAssertions.append(b); }
  labProps.replaceChildren();
  if (labSelected?.kind === 'node') {
    const n = labTopology.nodes.find(x => x.id === labSelected.id);
    if (n) { const name = document.createElement('input'); name.value = n.name; name.setAttribute('aria-label', 'Node name'); name.onchange = () => { n.name = name.value.trim() || n.id; labRender(); };
      const role = document.createElement('select'); for (const v of ['host', 'router', 'switch']) role.append(new Option(v, v)); role.value = n.role; role.onchange = () => { n.role = role.value; labRender(); };
      const restart = element('button', 'Restart node'); restart.onclick = async () => { try { await api('labs', 'POST', { action: 'restart-node', node: n.id }); labSay('Node restart requested.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
      const nodeState = labRuntimeStatus?.nodes?.[n.id]; const nodeMetrics = nodeState ? 'CPU ' + (Number(nodeState.cpu_percent) || 0).toFixed(1) + '% · memory ' + bytes(Number(nodeState.memory_usage_bytes) || 0) + ' / ' + bytes(Number(nodeState.memory_limit_bytes) || 0) : 'No live telemetry'; labProps.append(element('strong', 'Node'), name, role, element('span', n.interfaces.length + ' interfaces' + (nodeState?.state ? ' · ' + nodeState.state : '')), element('span', nodeMetrics), restart);
      const daemonLabel = element('strong', 'Daemons'); labProps.append(daemonLabel);
      const enabledDaemons = new Set(n.daemons || []);
      for (const daemon of ['frr', 'mstpd', 'keepalived', 'hostapd', 'wpa_supplicant', 'snmpd', 'sflow']) { const label = document.createElement('label'), checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.checked = enabledDaemons.has(daemon); checkbox.onchange = () => { n.daemons ||= []; n.daemons = checkbox.checked ? [...new Set([...n.daemons, daemon])] : n.daemons.filter(x => x !== daemon); if (!checkbox.checked) delete n.daemon_config?.[daemon]; labRender(); }; label.append(checkbox, document.createTextNode(daemon)); labProps.append(label); }
      n.daemon_config ||= {};
      for (const iface of n.interfaces) { const ifaceLabel = element('strong', 'Interface ' + iface.id); const addr = document.createElement('input'); addr.value = (iface.addresses || []).join(', '); addr.placeholder = 'Addresses, e.g. 10.0.0.1/24'; addr.onchange = () => { iface.addresses = addr.value.split(',').map(x => x.trim()).filter(Boolean); };
        const mac = document.createElement('input'); mac.value = iface.mac || ''; mac.placeholder = 'MAC address (optional)'; mac.onchange = () => { if (mac.value.trim()) iface.mac = mac.value.trim(); else delete iface.mac; };
        const mtu = document.createElement('input'); mtu.type = 'number'; mtu.min = '576'; mtu.max = '65535'; mtu.placeholder = 'MTU'; mtu.value = iface.mtu || ''; mtu.onchange = () => { if (Number(mtu.value)) iface.mtu = Number(mtu.value); else delete iface.mtu; };
        const faultKey=n.id+'/'+iface.id, fault=labInterfaceFaults.get(faultKey)||{loss_pct:0,corrupt_pct:0};
        const errorRate=document.createElement('input');errorRate.type='number';errorRate.min='0';errorRate.max='100';errorRate.step='0.1';errorRate.value=fault.loss_pct||0;errorRate.setAttribute('aria-label','Interface packet loss percent');
        const corrupt=document.createElement('input');corrupt.type='number';corrupt.min='0';corrupt.max='100';corrupt.step='0.1';corrupt.value=fault.corrupt_pct||0;corrupt.setAttribute('aria-label','Interface packet corruption percent');
        const inject=element('button','Inject interface errors');inject.onclick=async()=>{const selected={loss_pct:Number(errorRate.value)||0,corrupt_pct:Number(corrupt.value)||0};labInterfaceFaults.set(faultKey,selected);try{await api('labs','POST',{action:'interface-errors',node:n.id,interface:iface.id,impairment:selected});labSay('Interface errors applied.');await labRefresh();}catch(e){labSay(e.message,true);}};
        labProps.append(ifaceLabel, addr, mac, mtu, element('label','Loss % '),errorRate,element('label','Corruption % '),corrupt,inject); }
      const addIface = element('button', 'Add interface'); addIface.onclick = () => { const id = labFreshID('eth', new Set(n.interfaces.map(x => x.id))); n.interfaces.push({ id, name: id, addresses: [] }); labRender(); }; labProps.append(addIface);
      for (const daemon of n.daemons || []) { const label = element('strong', daemon + ' configuration'); const area = document.createElement('textarea'); area.rows = 5; area.cols = 56; area.value = n.daemon_config[daemon] || ''; area.setAttribute('aria-label', daemon + ' config'); area.onchange = () => { if (area.value) n.daemon_config[daemon] = area.value; else delete n.daemon_config[daemon]; }; labProps.append(label, area); }
    }
  } else if (labSelected?.kind === 'link') {
    const l = labTopology.links.find(x => x.id === labSelected.id);
    if (l) { const inp = (label, field, type, max) => { const wrap = element('label', label + ' '); const v = document.createElement('input'); v.type = 'number'; v.min = '0'; v.max = String(max); v.step = type === 'number' ? '0.1' : '1'; v.value = l.impairment[field] || 0; v.onchange = () => { l.impairment[field] = type === 'number' ? Number(v.value) : Math.round(Number(v.value)); labRender(); }; wrap.append(v); return wrap; };
      const toggle = element('button', 'Toggle link up/down'); toggle.onclick = async () => { const action = labRuntimeStatus?.links?.[l.id]?.up === false ? 'link-up' : 'link-down'; try { await api('labs', 'POST', { action, link: l.id }); labSay('Link state changed.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
      const flap = element('button', 'Flap link (1 s)'); flap.onclick = async () => { try { await api('labs', 'POST', { action: 'link-flap', link: l.id, duration_ms: 1000 }); labSay('Link flap started.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
      const capture = element('button', 'Capture 5 s'); capture.onclick = async () => { try { labSay('Capturing link…'); const out = await api('labs', 'POST', { action: 'capture', link: l.id, duration_ms: 5000 }); const binary = atob(out.pcap_base64 || ''), bytes = new Uint8Array(binary.length); for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i); const url = URL.createObjectURL(new Blob([bytes], { type: 'application/vnd.tcpdump.pcap' })), a = document.createElement('a'); a.href = url; a.download = l.id + '.pcap'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); labSay('Packet capture downloaded.'); } catch (e) { labSay(e.message, true); } };
      const applyImpairment = element('button', 'Apply link settings'); applyImpairment.onclick = async () => { try { await api('labs', 'POST', { action: 'set-impairment', link: l.id, impairment: l.impairment }); labSay('Link settings applied.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
      const stats = labRuntimeStatus?.links?.[l.id]; labProps.append(element('strong', 'Link'), inp('Latency ms', 'latency_ms', 'int', 60000), inp('Jitter ms', 'jitter_ms', 'int', 60000), inp('Loss %', 'loss_pct', 'number', 100), inp('Corruption %', 'corrupt_pct', 'number', 100), inp('Bandwidth bps', 'bandwidth_bps', 'int', 1e12), element('span', stats ? JSON.stringify(stats).slice(0, 160) : 'No live telemetry'), applyImpairment, toggle, flap, capture); }
  } else if (labSelected?.kind === 'assertion') {
    const a = labTopology.assertions.find(x => x.id === labSelected.id);
    if (a) { const kind = document.createElement('select'); for (const v of ['ping', 'traceroute', 'ospf-neighbor', 'bgp-neighbor', 'interface-up']) kind.append(new Option(v, v)); kind.value = a.kind; kind.onchange = () => { a.kind = kind.value; labRender(); }; const target = document.createElement('input'); target.value = a.target || ''; target.placeholder = 'Target / interface'; target.onchange = () => { a.target = target.value.trim(); }; const expected = document.createElement('input'); expected.value = a.expected || ''; expected.placeholder = 'Expected result (optional)'; expected.onchange = () => { a.expected = expected.value.trim(); }; labProps.append(element('strong', 'Validation check'), kind, target, expected); }
  }
  labToolbarStatus.textContent = labMode === 'link' ? 'Link mode: choose two nodes' : labTopology.nodes.length + ' nodes · ' + labTopology.links.length + ' links';
}
labCanvas.onpointermove = e => { if (!labDrag) return; const n = labTopology.nodes.find(x => x.id === labDrag.id); if (!n) return; const pt = labCanvas.createSVGPoint(); pt.x = e.clientX; pt.y = e.clientY; const p = pt.matrixTransform(labCanvas.getScreenCTM().inverse()); n.position.x = Math.max(10, Math.min(910, p.x - labDrag.dx)); n.position.y = Math.max(10, Math.min(380, p.y - labDrag.dy)); labRender(); };
labCanvas.onpointerup = () => { labDrag = null; };
labAddHost.onclick = () => labAddNode('host'); labAddRouter.onclick = () => labAddNode('router'); labAddSwitch.onclick = () => labAddNode('switch');
labLinkMode.onclick = () => { labMode = 'link'; labLinkStart = null; labRender(); labSay('Select the first node.'); };
labAddCheck.onclick = () => { const node = labTopology.nodes.find(n => n.id === labSelected?.id) || labTopology.nodes[0]; if (!node) { labSay('Add a node first.', true); return; } labTopology.assertions ||= []; const id = labFreshID('check', new Set(labTopology.assertions.map(a => a.id))); labTopology.assertions.push({ id, node: node.id, kind: 'ping', target: '127.0.0.1' }); labSelected = { kind: 'assertion', id }; labRender(); };
labRunChecks.onclick = async () => { try { const out = await api('labs', 'POST', { action: 'check' }); labSay((out.passed || 0) + '/' + (out.total || 0) + ' checks passed.'); } catch (e) { labSay(e.message, true); } };
labNew.onclick = () => { labTopology = { version: 1, id: 'new-lab', name: 'New lab', nodes: [], links: [] }; labName.value = labTopology.name; labSelected = null; labRender(); labSay('New topology.'); };
labSave.onclick = async () => { const id = labName.value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 32); if (!id || !/^[a-z]/.test(id)) { labSay('Topology name must start with a letter and use letters, numbers, or spaces.', true); return; } labTopology.id = id; labTopology.name = labName.value.trim(); try { await api('labs', 'POST', { action: 'save', topology: labTopology }); labSay('Topology saved on the server.'); } catch (e) { labSay(e.message, true); } };
labLoad.onclick = async () => { try { const data = await api('labs'); const list = data.topologies || []; if (!list.length) { labSay('No saved topologies yet.'); return; } const choice = prompt('Enter a topology ID to load:\n' + list.map(t => t.id + ' — ' + t.name).join('\n'), list[0].id); if (choice == null) return; const found = list.find(t => t.id === choice); if (!found) throw new Error('Topology ID was not found.'); labTopology = found; labName.value = found.name; labSelected = null; labRender(); labSay('Topology loaded.'); } catch (e) { labSay(e.message, true); } };
labApply.onclick = async () => { labTopology.name = labName.value.trim(); try { await api('labs', 'POST', { action: 'apply', topology: labTopology }); labSay('Topology applied.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
labRollback.onclick = async () => { try { await api('labs', 'POST', { action: 'rollback' }); labSay('Previous topology restored.'); await labRefresh(); } catch (e) { labSay(e.message, true); } };
async function labRefresh() { try { labRuntimeStatus = await api('labs?view=status'); labRender(); } catch (_) { /* Status is optional until a runtime backend is configured. */ } }
labRender();
setInterval(() => { if (scannerSub === 'topology' && topologyMode === 'lab' && $('scanner').classList.contains('active')) labRefresh(); }, 3000);

let selectedDevice = null, usageRange = 60;
const linkRates = new Map(); // slot -> { at, tx, rx, txBps, rxBps }
const exitRates = new Map(); // exit address -> { at, tx, rx, txBps, rxBps }

function updateLinkRates(paths) {
  const now = Date.now();
  for (const p of paths) {
    const prev = linkRates.get(p.slot), tx = Number(p.wire_tx_bytes) || 0, rx = Number(p.wire_rx_bytes) || 0;
    if (prev && now - prev.at >= 800) {
      const dt = (now - prev.at) / 1000;
      linkRates.set(p.slot, { at: now, tx, rx, txBps: Math.max(0, tx - prev.tx) * 8 / dt, rxBps: Math.max(0, rx - prev.rx) * 8 / dt });
    } else if (!prev) linkRates.set(p.slot, { at: now, tx, rx, txBps: 0, rxBps: 0 });
  }
}

function updateExitRates(servers) {
  const now = Date.now();
  for (const x of servers) {
    const prev = exitRates.get(x.address), tx = Number(x.wire_tx_bytes) || 0, rx = Number(x.wire_rx_bytes) || 0;
    if (prev && now - prev.at >= 800) {
      const dt = (now - prev.at) / 1000;
      exitRates.set(x.address, { at: now, tx, rx, txBps: Math.max(0, tx - prev.tx) * 8 / dt, rxBps: Math.max(0, rx - prev.rx) * 8 / dt });
    } else if (!prev) exitRates.set(x.address, { at: now, tx, rx, txBps: 0, rxBps: 0 });
  }
}

// Build the node list: tunnel-routed devices (measured) and other LAN devices (not visible).
function topologyDevices() {
  const usage = current?.device_usage || {}, routed = [], seen = new Set();
  for (const d of current?.lan_devices || []) {
    const key = 'ip:' + d.tunnel_ip;
    if (d.device_mac) seen.add(macKey(d.device_mac));
    routed.push({ key, routed: true, name: d.name || d.tunnel_ip, ip: d.tunnel_ip, mac: d.device_mac || '', adapterMac: d.adapter_mac || '', up: d.up !== false && !d.error,
      present: d.present, leased: d.leased, silent: d.silent_s, error: d.error, lease: d.lease, usage: usage[d.tunnel_ip] || null });
  }
  const others = [];
  for (const d of deviceInventory?.devices || []) {
    if (d.mac && seen.has(macKey(d.mac))) continue;
    if (!d.reachable_now && d.state !== 'Reachable') continue;
    others.push({ key: 'lan:' + (d.mac || d.address), routed: false, name: d.hostname || d.vendor || d.address, ip: d.address, mac: d.mac || '', vendor: d.vendor || '', up: true,
      first: d.first_seen, last: d.last_seen });
  }
  return { routed, others: others.slice(0, 12), extra: Math.max(0, others.length - 12) };
}

// Layer 2 bridge: frame counters from the client's l2_bridge block, turned into rates between renders.
const l2Prev = { at: 0, to: 0, from: 0, toBps: 0, fromBps: 0 };
function l2Rates(b) {
  const now = performance.now() / 1000, to = Number(b.to_tunnel) || 0, from = Number(b.from_tunnel) || 0;
  if (l2Prev.at && now - l2Prev.at >= 0.5) {
    const dt = now - l2Prev.at;
    l2Prev.toBps = Math.max(0, to - l2Prev.to) / dt; l2Prev.fromBps = Math.max(0, from - l2Prev.from) / dt;
    l2Prev.at = now; l2Prev.to = to; l2Prev.from = from;
  } else if (!l2Prev.at) { l2Prev.at = now; l2Prev.to = to; l2Prev.from = from; }
  return { toFps: l2Prev.toBps, fromFps: l2Prev.fromBps };
}

function renderNetworkMetrics() {
  topoNetworkMetrics.replaceChildren();
  const local = current?.local_networks || {}, networks = Array.isArray(local.networks) ? local.networks : [], stats = local.stats || {};
  const metricCard = (title, values, note) => {
    const card = element('section', null, { class: 'topo-metric-card' });
    card.append(element('h3', title));
    const grid = element('div', null, { class: 'topo-metric-grid' });
    for (const [label, value] of values) {
      const tile = element('div', null, { class: 'topo-metric' });
      tile.append(element('b', String(value)), element('span', label)); grid.append(tile);
    }
    card.append(grid);
    if (note) card.append(element('p', note, { class: 'topo-metric-note' }));
    return card;
  };
  const count = value => (Number(value) || 0).toLocaleString();
  if (networks.length) {
    topoNetworkMetrics.append(metricCard('Local L3 forwarding', [
      ['Networks up', networks.filter(n => n.up).length + ' / ' + networks.length], ['Packets routed', count(stats.PacketsRouted)],
      ['To tunnel', count(stats.PacketsToTunnel)], ['From tunnel', count(stats.PacketsFromTunnel)],
      ['Source guard drops', count(stats.SpoofedSourceDropped)], ['No route', count(stats.NoRoute)],
      ['Inter-network denied', count(stats.InterNetworkDenied)], ['TTL expired', count(stats.TTLExpired)],
      ['VLAN rejected', count(stats.VLANRejected)], ['Queue drops', count(stats.QueueDrops)],
    ], 'Forwarding and drop counters since the client started.'));
    const card = element('section', null, { class: 'topo-metric-card' });
    card.append(element('h3', 'Local networks · DHCP and relay'));
    for (const n of networks) {
      const row = element('div', null, { class: 'topo-network-row' });
      const vlan = n.vlan_id ? ' · VLAN ' + n.vlan_id : '', state = n.up ? 'up' : 'down';
      row.append(element('b', (n.name || 'Network') + ' · ' + state), element('div', n.prefix + vlan + ' · ' + count(n.neighbors) + ' neighbors'));
      if (n.dhcp_pool) row.append(element('div', 'DHCP leases ' + count(n.dhcp_pool.leased) + ' / ' + count(n.dhcp_pool.size) + ' · free ' + count(n.dhcp_pool.free)));
      if (n.dhcp_stats) row.append(element('div', 'DHCP offers ' + count(n.dhcp_stats.Offers) + ' · ACKs ' + count(n.dhcp_stats.Acks) + ' · NAKs ' + count(n.dhcp_stats.NAKs)));
      if (n.relay_stats) row.append(element('div', 'Relay requests ' + count(n.relay_stats.Requests) + ' · replies ' + count(n.relay_stats.Replies) + ' · rejected ' + count(n.relay_stats.Rejected) + ' · expired ' + count(n.relay_stats.Expired)));
      card.append(row);
    }
    topoNetworkMetrics.append(card);
  }
  const l2 = current?.l2_bridge;
  if (l2) {
    topoNetworkMetrics.append(metricCard('Layer 2 bridge', [
      ['State', l2.up ? 'up' : 'down'], ['MAC table', count(l2.fdb_size)], ['Learned', count(l2.learned)], ['Moves', count(l2.moves)], ['Aged out', count(l2.aged)],
      ['Forwarded', count(l2.forwarded)], ['Flooded', count(l2.flooded)], ['Frames to exit', count(l2.to_tunnel)], ['Frames from exit', count(l2.from_tunnel)], ['Orphan frames', count(l2.orphans)],
    ], 'Orphans are L2 payloads that arrived with no bridge up yet.'));
    // Every drop reason the switch tracks, not a single lumped total: a blocked port and a storm
    // limit need different fixes, and lumping them hides which one is actually happening.
    const dropReasons = [
      ['drop_storm', 'Storm limit'], ['drop_mac_limit', 'MAC limit'], ['drop_blocked', 'Port blocked'], ['drop_isolated', 'PVLAN isolated'],
      ['drop_vlan', 'VLAN mismatch'], ['drop_bpdu_guard', 'BPDU guard'], ['drop_rstp', 'RSTP not forwarding'], ['drop_ra_guard', 'RA guard'],
      ['drop_source_guard', 'Source guard'], ['drop_copp', 'Control-plane limit'], ['drop_unauthorized', '802.1X unauthorized'], ['drop_other', 'Other'],
      ['switch_drops', 'Classify/queue error'], ['queue_full', 'Tunnel queue full'], ['oversize', 'Oversize frame'], ['write_errors', 'Adapter write error'],
    ].map(([key, label]) => [label, Number(l2[key]) || 0]).filter(([, v]) => v > 0);
    const dropCard = element('section', null, { class: 'topo-metric-card' });
    dropCard.append(element('h3', 'Layer 2 bridge · drops by reason'));
    if (dropReasons.length) {
      const list = element('div');
      for (const [label, v] of dropReasons.sort((a, b) => b[1] - a[1])) list.append(element('div', label + ': ' + count(v), { class: 'topo-network-row' }));
      dropCard.append(list);
    } else dropCard.append(element('p', 'No drops of any kind since the client started.', { class: 'topo-metric-note' }));
    topoNetworkMetrics.append(dropCard);
    const frames = l2.frames || {}, top = Object.entries(frames).sort((a, b) => b[1] - a[1]).slice(0, 10);
    if (top.length) {
      const frameCard = element('section', null, { class: 'topo-metric-card' });
      frameCard.append(element('h3', 'Layer 2 bridge · frames by verdict and class'));
      for (const [key, v] of top) frameCard.append(element('div', key + ': ' + count(v), { class: 'topo-network-row' }));
      topoNetworkMetrics.append(frameCard);
    }
    if (Array.isArray(l2.recent) && l2.recent.length) {
      const details = element('details', null, { class: 'topo-metric-card' });
      details.append(element('summary', 'Layer 2 bridge · recent frames (' + l2.recent.length + ')'));
      const pre = element('pre', l2.recent.join('\n'), { class: 'topo-frame-log' });
      details.append(pre);
      topoNetworkMetrics.append(details);
    }
  }
  if (!networks.length && !current?.l2_bridge) topoNetworkMetrics.append(element('p', 'No local L2 bridge or routed local networks are active.', { class: 'notice' }));
}

function overallHealth(paths) {
  if (!paths.length) return ['poor', 'The tunnel is not connected'];
  const down = paths.filter(p => !p.healthy);
  if (!down.length) return ['good', 'Everything is great!'];
  if (down.length === paths.length) return ['poor', 'All links are down'];
  return ['fair', down.map(p => 'Link ' + (p.slot + 1)).join(', ') + (down.length > 1 ? ' are' : ' is') + ' down; running on the other line'];
}

// activePorts summarises what a routed device is talking on right now: the remote-side ports that carried
// a packet within the last few seconds (the client marks them active). It reports only what the tunnel sees.
function activePorts(usage) {
  if (!usage) return { line: '', tip: '' };
  const act = (usage.ports || []).filter(r => r.active && r.port);
  if (!act.length) return { line: 'No active ports', tip: 'No ports carried traffic in the last 10 seconds' };
  const label = r => String(r.port), parts = [];
  let line = 'Ports ';
  for (const r of act) {
    const next = (parts.length ? line + ' ' : line) + label(r), more = act.length - parts.length - 1;
    if (next.length + (more > 0 ? 3 + String(more).length : 0) > 32 && parts.length) break;
    line = next; parts.push(r);
  }
  if (act.length > parts.length) line += ' +' + (act.length - parts.length);
  const tip = act.map(r => String(r.proto).toUpperCase() + ' ' + r.port + (r.service ? ' ' + r.service : '') + ' · ↓ ' + bytes(r.down_bytes) + ' ↑ ' + bytes(r.up_bytes)).join('\n');
  return { line, tip: 'Active remote ports (traffic in the last 10 seconds)\n' + tip };
}

// Cuts to n characters and marks it with an ellipsis so a clipped line never reads as complete
// (e.g. "seen 0" instead of "seen 0s ago" silently dropping the unit).
function clip(text, n) { text = String(text || ''); return text.length > n ? text.slice(0, n - 1) + '…' : text; }
function nodeBox(x, y, w, h, title, subtitle, state, onActivate, selected, extraLine, tip) {
  const g = svg('g', { class: 'topo-node' + (selected ? ' sel' : ''), transform: 'translate(' + x + ',' + y + ')' });
  const color = state === 'good' ? '#2fb800' : state === 'poor' ? '#e0201b' : state === 'fair' ? '#f5a300' : '#9aa1ab';
  g.append(svg('rect', { class: 'tb-rect', width: w, height: h, rx: 8 }), svg('circle', { cx: 12, cy: 14, r: 4, fill: color }),
    svg('text', { class: 'tb-title', x: 22, y: 18, 'font-size': 12 }, clip(title, 27)), svg('text', { class: 'tb-sub', x: 12, y: 36, 'font-size': 11 }, clip(subtitle, 26)));
  if (tip) g.append(svg('title', {}, tip));
  (Array.isArray(extraLine) ? extraLine : extraLine ? [extraLine] : []).forEach((t, i) => g.append(svg('text', { class: 'tb-sub', x: 12, y: 51 + i * 13, 'font-size': 10 }, clip(t, 32))));
  if (onActivate) {
    g.setAttribute('tabindex', '0'); g.setAttribute('role', 'button'); g.setAttribute('aria-label', title + (subtitle ? ', ' + subtitle : ''));
    g.onclick = onActivate; g.onkeydown = e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onActivate(); } };
  }
  return g;
}
const liveLayoutKey = 'gamebond.liveTopology.positions.v1';
let livePositions = new Map();
try {
  const saved = JSON.parse(localStorage.getItem(liveLayoutKey) || '{}');
  for (const [id, p] of Object.entries(saved)) if (Number.isFinite(p?.x) && Number.isFinite(p?.y)) livePositions.set(id, p);
} catch (_) { /* Layout preference is optional when storage is unavailable. */ }
let liveDrag = null;
function liveNodeBox(id, x, y, w, h, ...args) {
  const saved = livePositions.get(id), px = saved?.x ?? x, py = saved?.y ?? y;
  const g = nodeBox(px, py, w, h, ...args);
  g.dataset.liveNode = id; g.dataset.baseX = x; g.dataset.baseY = y; g.dataset.width = w; g.dataset.height = h;
  g.setAttribute('aria-description', 'Drag to change layout. Position is saved on this device.');
  g.addEventListener('pointerdown', e => {
    if (topologyMode !== 'live' || e.button !== 0 || e.target.closest('a,button')) return;
    const pt = topoMap.createSVGPoint(); pt.x = e.clientX; pt.y = e.clientY;
    const p = pt.matrixTransform(topoMap.getScreenCTM().inverse());
    liveDrag = { id, dx: p.x - px, dy: p.y - py, pointerId: e.pointerId };
    topoMap.setPointerCapture(e.pointerId); e.preventDefault();
  });
  return g;
}
function saveLivePositions() {
  try { localStorage.setItem(liveLayoutKey, JSON.stringify(Object.fromEntries(livePositions))); } catch (_) { /* Layout preference is optional. */ }
}
topoMap.addEventListener('pointermove', e => {
  if (!liveDrag || liveDrag.pointerId !== e.pointerId || topologyMode !== 'live') return;
  const pt = topoMap.createSVGPoint(); pt.x = e.clientX; pt.y = e.clientY;
  const p = pt.matrixTransform(topoMap.getScreenCTM().inverse());
  livePositions.set(liveDrag.id, { x: Math.max(0, Math.min(880, p.x - liveDrag.dx)), y: Math.max(0, Math.min(10000, p.y - liveDrag.dy)) });
  renderTopology();
});
function finishLiveDrag(e) {
  if (!liveDrag || e.pointerId !== liveDrag.pointerId) return;
  liveDrag = null; saveLivePositions();
}
topoMap.addEventListener('pointerup', finishLiveDrag);
topoMap.addEventListener('pointercancel', finishLiveDrag);
function syncLiveEdges() {
  const nodes = [...topoMap.querySelectorAll('[data-live-node]')].map(g => ({
    x: Number(g.dataset.baseX), y: Number(g.dataset.baseY), w: Number(g.dataset.width), h: Number(g.dataset.height),
    px: Number(g.getAttribute('transform').match(/translate\(([^,]+)/)?.[1]),
    py: Number(g.getAttribute('transform').match(/,([^)]*)\)/)?.[1])
  }));
  const moveEndpoint = (x, y) => {
    let best = null, distance = Infinity;
    for (const n of nodes) {
      const cx = Math.max(n.x, Math.min(n.x + n.w, x)), cy = Math.max(n.y, Math.min(n.y + n.h, y));
      const d = Math.hypot(cx - x, cy - y);
      if (d < distance) { best = { x: n.px + (x - n.x), y: n.py + (y - n.y) }; distance = d; }
    }
    return distance <= 5 ? best : { x, y };
  };
  for (const line of topoMap.querySelectorAll('line')) {
    const x1 = Number(line.getAttribute('x1')), y1 = Number(line.getAttribute('y1'));
    const x2 = Number(line.getAttribute('x2')), y2 = Number(line.getAttribute('y2'));
    const a = moveEndpoint(x1, y1), b = moveEndpoint(x2, y2);
    line.setAttribute('x1', a.x); line.setAttribute('y1', a.y); line.setAttribute('x2', b.x); line.setAttribute('y2', b.y);
  }
}
// Traffic intensity 0..1 on a log scale: ~10 kbps is a faint trickle, 100 Mbps is full.
function flowIntensity(bps) {
  bps = Number(bps) || 0;
  if (bps < 2e3) return 0;
  return Math.min(1, Math.max(0.08, Math.log10(bps / 1e4) / 4 + 0.12));
}
// One direction of a connection: dashes travel along it, a glow pulses over them, and a soft halo
// sits underneath. Speed, thickness and glow all grow with the traffic. The animation phase comes
// from the wall clock so a redraw does not restart it.
//
// Traffic spikes would make the glow jump, so each connection keeps a displayed intensity that eases
// toward the measured one every frame: quickly when traffic rises (tau 0.5 s), slowly when it falls
// (tau 1.6 s). The map is redrawn on every status update; the frame loop below keeps the live
// elements of the latest redraw moving between the redraws.
const flowState = new Map(); // key -> { cur, target, els, seen, phase, forward }
// Dash travel speed in px/s from the smoothed intensity: a slow crawl for a trickle, a rush at line rate.
const flowSpeed = i => 8 + 60 * i + 130 * i * i;
let flowFrame = 0, flowLast = 0, flowLoop = false;
function flowTick(now) {
  const dt = Math.min(0.25, (now - (flowLast || now)) / 1000);
  flowLast = now;
  for (const [key, st] of flowState) {
    const tau = st.target > st.cur ? 0.5 : 1.6;
    st.cur += (st.target - st.cur) * (1 - Math.exp(-dt / tau));
    if (Math.abs(st.target - st.cur) < 0.002) st.cur = st.target;
    for (const el of st.els) el.style.setProperty('--i', st.cur.toFixed(3));
    // The dashes advance by speed x time, so a change of speed bends the motion instead of restarting it.
    if (!(window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches)) st.phase = (st.phase + flowSpeed(st.cur) * dt) % 16;
    if (st.els[1]) st.els[1].setAttribute('stroke-dashoffset', (st.forward ? -st.phase : st.phase).toFixed(2));
    if (Date.now() - st.seen > 10000) flowState.delete(key);
  }
  requestAnimationFrame(flowTick);
}
function flowLayer(g, x1, y1, x2, y2, bps, forward) {
  const key = [x1, y1, x2, y2, forward].join(',');
  let st = flowState.get(key);
  if (!st) { st = { cur: 0, target: 0, els: [], seen: 0, phase: 0, forward }; flowState.set(key, st); }
  st.target = flowIntensity(bps); st.seen = Date.now(); st.els = [];
  if (!flowLoop) { flowLoop = true; requestAnimationFrame(flowTick); }
  const i = st.cur;
  if (i < 0.02 && !st.target) return; // faded out and idle: nothing to draw
  const dx = x2 - x1, dy = y2 - y1, len = Math.hypot(dx, dy) || 1, off = (forward ? 3 : -3), ox = -dy / len * off, oy = dx / len * off;
  const dur = 1.8 - 1.45 * Math.max(i, 0.05), t = (Date.now() / 1000), cls = forward ? 'down' : 'up';
  const attrs = { x1: x1 + ox, y1: y1 + oy, x2: x2 + ox, y2: y2 + oy };
  const halo = svg('line', { ...attrs, class: 'tf-halo ' + cls });
  halo.style.setProperty('--i', i.toFixed(3));
  halo.style.animation = 'tf-halo-pulse ' + (dur * 3).toFixed(2) + 's ease-in-out infinite';
  halo.style.animationDelay = '-' + (t % (dur * 3)).toFixed(3) + 's';
  const line = svg('line', { ...attrs, class: 'tf ' + cls, 'stroke-dashoffset': (forward ? -st.phase : st.phase).toFixed(2) });
  line.style.setProperty('--i', i.toFixed(3));
  line.style.animation = 'tf-glow ' + (dur * 3).toFixed(2) + 's ease-in-out infinite'; // the dashes are moved by flowTick
  line.style.animationDelay = '-' + (t % (dur * 3)).toFixed(3) + 's';
  const title = svg('title'); title.textContent = (forward ? 'Download ' : 'Upload ') + fmtBps(bps);
  line.append(title);
  st.els = [halo, line];
  g.append(halo, line);
}
// flow = { down, up } in bps; down runs from (x1,y1) to (x2,y2), up the other way.
function mapLine(x1, y1, x2, y2, dashed, label, flow) {
  const g = svg('g');
  g.append(svg('line', { class: 'tl', x1, y1, x2, y2, 'stroke-width': 2, 'stroke-dasharray': dashed ? '4 4' : '' }));
  if (flow) { flowLayer(g, x1, y1, x2, y2, flow.down, true); flowLayer(g, x1, y1, x2, y2, flow.up, false); }
  if (label) g.append(svg('text', { x: (x1 + x2) / 2, y: (y1 + y2) / 2 - 5, 'font-size': 10, 'text-anchor': 'middle', class: 'tl-label' }, label));
  return g;
}

const pieColors = ['#0f7ae5', '#2fb800', '#f5a300', '#9b59d0', '#e0201b', '#00a5b8', '#8c6d46', '#7b8089'];
// A donut with a legend, like the UniFi dashboard's Devices / Clients / Deep Packet Inspection rings.
// slices: [{ label, value, text }]; the centre shows the total.
function donutCard(title, slices, centerValue, centerLabel, note) {
  const card = element('div', null, { class: 'pie-card' }), body = element('div', null, { class: 'pie-body' }), legend = element('div', null, { class: 'pie-legend' });
  card.append(element('h3', title));
  const live = slices.filter(x => x.value > 0), total = live.reduce((a, x) => a + x.value, 0), r = 44, c = 2 * Math.PI * r;
  const ring = svg('svg', { viewBox: '0 0 120 120', class: 'pie-svg', role: 'img', 'aria-label': title + ': ' + centerValue + ' ' + centerLabel });
  ring.append(svg('circle', { cx: 60, cy: 60, r, fill: 'none', class: 'pie-track', 'stroke-width': 16 }));
  let off = 0;
  live.forEach((x, i) => {
    x.color = x.color || pieColors[i % pieColors.length];
    const len = x.value / total * c, seg = svg('circle', { cx: 60, cy: 60, r, fill: 'none', stroke: x.color, 'stroke-width': 16, 'stroke-dasharray': len.toFixed(2) + ' ' + (c - len).toFixed(2), 'stroke-dashoffset': (-off).toFixed(2), transform: 'rotate(-90 60 60)' });
    seg.append(svg('title', {}, x.label + ': ' + x.text)); ring.append(seg); off += len;
    const row = element('div'), sw = element('i'); sw.style.background = x.color;
    row.append(sw, element('span', x.label), element('b', x.text)); legend.append(row);
  });
  if (!live.length) legend.append(element('p', 'Nothing to show yet', { class: 'sub' }));
  ring.append(svg('text', { x: 60, y: 58, 'text-anchor': 'middle', class: 'pie-num', 'font-size': centerValue.length > 5 ? 13 : centerValue.length > 3 ? 16 : 20 }, centerValue), svg('text', { x: 60, y: 73, 'text-anchor': 'middle', class: 'tb-sub', 'font-size': 8 }, centerLabel));
  body.append(ring, legend); card.append(body);
  if (note) card.append(element('p', note, { class: 'sub' }));
  return card;
}

function renderPies(routed, others, extra, bridged = []) {
  const online = routed.filter(d => d.up && d.present !== false).length, offline = routed.length - online, lanOnly = others.length + extra;
  const total = routed.length + lanOnly + bridged.length;
  const slices = [
    { label: 'Routed · online', value: online, text: String(online), color: '#2fb800' }, { label: 'Routed · offline', value: offline, text: String(offline), color: '#e0201b' },
    { label: 'LAN only', value: lanOnly, text: String(lanOnly), color: '#9aa1ab' }];
  if (bridged.length) slices.push({ label: 'Bridged (L2)', value: bridged.length, text: String(bridged.length), color: '#0f7ae5' });
  const devices = donutCard('Devices', slices, String(total), total === 1 ? 'device' : 'devices',
    'Routed devices go through GameBond; LAN-only devices are seen by the scanner but not routed; bridged devices carry their own MAC through the Layer 2 bridge.');
  // Clients by vendor: the scanner's inventory knows the vendor for a MAC; anything it cannot name is "Unknown".
  const byMac = new Map((deviceInventory?.devices || []).map(x => [macKey(x.mac), x.vendor || '']));
  const counts = new Map();
  for (const d of [...routed, ...others]) {
    const v = (d.vendor || byMac.get(macKey(d.mac)) || '').trim() || 'Unknown';
    counts.set(v, (counts.get(v) || 0) + 1);
  }
  let vendors = [...counts].sort((a, b) => b[1] - a[1]);
  if (vendors.length > 7) vendors = [...vendors.slice(0, 6), ['Other', vendors.slice(6).reduce((a, x) => a + x[1], 0)]];
  const clients = donutCard('Clients', vendors.map(([label, value]) => ({ label, value, text: String(value) })), String(total), total === 1 ? 'client' : 'clients', 'Grouped by the vendor the scanner reports for each MAC address.');
  // Traffic by class, from each device's port totals since the client started. Classes come from the remote
  // port alone; the tunnel does not inspect packets.
  const cat = new Map();
  for (const u of Object.values(current?.device_usage || {})) for (const c of u.categories || []) cat.set(c.category, (cat.get(c.category) || 0) + (c.up_bytes || 0) + (c.down_bytes || 0));
  const classes = [...cat].sort((a, b) => b[1] - a[1]), sum = classes.reduce((a, x) => a + x[1], 0);
  const dpi = donutCard('Traffic by type', classes.map(([label, value]) => ({ label, value, text: bytes(value) })), bytes(sum), 'traffic', 'Classified by remote port, not by inspecting packets. Counts routed devices since the client started.');
  topoPies.replaceChildren(devices, clients, dpi);
}

// Smooth path through points (Catmull-Rom to cubic Bezier), kept inside [minY, maxY] so a curve never dips below the axis.
function smoothPath(pts, minY, maxY) {
  if (pts.length < 2) return pts.length ? 'M' + pts[0][0].toFixed(1) + ' ' + pts[0][1].toFixed(1) : '';
  const clamp = y => Math.min(maxY, Math.max(minY, y)), f = v => v.toFixed(1);
  let d = 'M' + f(pts[0][0]) + ' ' + f(pts[0][1]);
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] || pts[i], p1 = pts[i], p2 = pts[i + 1], p3 = pts[i + 2] || p2;
    d += 'C' + f(p1[0] + (p2[0] - p0[0]) / 6) + ' ' + f(clamp(p1[1] + (p2[1] - p0[1]) / 6)) + ' ' + f(p2[0] - (p3[0] - p1[0]) / 6) + ' ' + f(clamp(p2[1] - (p3[1] - p1[1]) / 6)) + ' ' + f(p2[0]) + ' ' + f(p2[1]);
  }
  return d;
}

let chartSeq = 0;
// Throughput as a soft gradient area (the thin line above it is the busiest minute in each bin) with the device's
// TCP handshake latency as a smooth line on the right-hand axis, in the style of the UniFi activity graphs.
function throughputChart(history, latency, minutes, dir) {
  const width = 520, height = 160, left = 44, right = 34, bottom = 18, bins = Math.min(minutes, 60), binMin = minutes / bins;
  const nowMin = Math.floor(Date.now() / 60000), start = nowMin - minutes + 1, mk = () => new Array(bins).fill(0);
  const sum = mk(), peak = mk(), lat = mk(), latN = mk(), idx = t => { const m = Math.floor(t / 60) - start; return m < 0 || m >= minutes ? -1 : Math.min(bins - 1, Math.floor(m / binMin)); };
  for (const [t, u, d] of history) { const i = idx(t); if (i < 0) continue; const v = dir === 'down' ? d : u; sum[i] += v; peak[i] = Math.max(peak[i], v * 8 / 60); }
  for (const [t, r] of latency) { const i = idx(t); if (i < 0) continue; lat[i] += r / 10; latN[i]++; }
  const avg = sum.map(v => v * 8 / (binMin * 60)), ms = lat.map((v, i) => latN[i] ? v / latN[i] : null);
  const top = Math.max(1e5, ...peak, ...avg) * 1.1, latTop = Math.max(10, ...ms.filter(v => v != null)) * 1.2, plot = height - bottom, usable = plot - 14, bw = (width - left - right) / bins;
  const color = dir === 'down' ? '#2fb800' : '#0f7ae5', gid = 'tg' + (++chartSeq), when = i => new Date((start + i * binMin) * 60000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const s = svg('svg', { class: 'topo-svg', viewBox: '0 0 ' + width + ' ' + height, role: 'img', 'aria-label': (dir === 'down' ? 'Download' : 'Upload') + ' throughput and latency' });
  s.style.maxWidth = '720px';
  const defs = svg('defs'), grad = svg('linearGradient', { id: gid, x1: 0, y1: 0, x2: 0, y2: 1 });
  grad.append(svg('stop', { offset: '0%', 'stop-color': color, 'stop-opacity': 0.5 }), svg('stop', { offset: '100%', 'stop-color': color, 'stop-opacity': 0.03 })); defs.append(grad); s.append(defs);
  for (let g = 0; g <= 4; g++) { const y = plot - g / 4 * usable; s.append(svg('line', { class: 'gl', x1: left, y1: y, x2: width - right, y2: y })); }
  s.append(svg('text', { class: 'tb-sub', x: 2, y: plot - usable + 3, 'font-size': 10 }, fmtBps(top)), svg('text', { class: 'tb-sub', x: 2, y: plot + 3, 'font-size': 10 }, '0'),
    svg('text', { class: 'tb-sub', x: width, y: plot - usable + 3, 'font-size': 10, 'text-anchor': 'end' }, Math.round(latTop) + ' ms'), svg('text', { class: 'tb-sub', x: width, y: plot + 3, 'font-size': 10, 'text-anchor': 'end' }, '0'),
    svg('text', { class: 'tb-sub', x: left, y: height - 4, 'font-size': 10 }, minutes >= 60 ? '-' + (minutes / 60) + ' h' : '-' + minutes + ' min'), svg('text', { class: 'tb-sub', x: width - right, y: height - 4, 'font-size': 10, 'text-anchor': 'end' }, 'now'));
  const cx = i => left + (i + 0.5) * bw, yOf = v => plot - v / top * usable;
  const areaPts = avg.map((v, i) => [cx(i), yOf(v)]), area = smoothPath(areaPts, plot - usable, plot);
  s.append(svg('path', { d: area + 'L' + cx(bins - 1).toFixed(1) + ' ' + plot + 'L' + cx(0).toFixed(1) + ' ' + plot + 'Z', fill: 'url(#' + gid + ')' }));
  s.append(svg('path', { d: area, fill: 'none', stroke: color, 'stroke-width': 1.6, 'stroke-linejoin': 'round' }));
  if (peak.some((v, i) => v > avg[i] * 1.05)) s.append(svg('path', { d: smoothPath(peak.map((v, i) => [cx(i), yOf(v)]), plot - usable, plot), fill: 'none', stroke: color, 'stroke-opacity': 0.4, 'stroke-width': 1, 'stroke-dasharray': '3 3' }));
  // Latency: runs of consecutive samples (a gap of up to two quiet bins is bridged) become smooth lines; a lone sample is a dot.
  const runs = []; let last = -99;
  ms.forEach((v, i) => { if (v == null) return; const pt = [cx(i), plot - v / latTop * usable]; if (i - last <= 3 && runs.length) runs[runs.length - 1].push(pt); else runs.push([pt]); last = i; });
  for (const run of runs) s.append(run.length > 1 ? svg('path', { d: smoothPath(run, plot - usable, plot), fill: 'none', stroke: '#f5a300', 'stroke-width': 1.6, 'stroke-linejoin': 'round' }) : svg('circle', { cx: run[0][0], cy: run[0][1], r: 2, fill: '#f5a300' }));
  for (let i = 0; i < bins; i++) { // one hover target per bin with the exact numbers
    const hit = svg('rect', { x: left + i * bw, y: 0, width: bw, height: plot, fill: 'transparent' });
    hit.append(svg('title', {}, when(i) + '\navg ' + fmtBps(avg[i]) + ' · max ' + fmtBps(peak[i]) + '\n' + (ms[i] != null ? 'latency ' + ms[i].toFixed(0) + ' ms' : 'latency: no TCP handshakes'))); s.append(hit);
  }
  return s;
}

// Friendly names for the exit hosts shown in the live map. Transport state keeps
// using the configured IP addresses; this only changes the labels people see.
const EXIT_HOSTNAMES = Object.freeze({
  '138.199.201.16': 'apps',
  '195.154.119.136': 'sw01',
  '194.164.124.139': 'ionos-exit-01'
});
const exitHostname = address => EXIT_HOSTNAMES[String(address || '')] || String(address || 'Unknown exit');

function renderTopology() {
  if (topologyMode !== 'live') return;
  if (!current || current.state === 'unavailable' && !current.transport) { topoHead.textContent = 'Status unavailable'; return; }
  const paths = current?.transport?.paths || [], { routed, others, extra } = topologyDevices();
  const l2All = current?.l2_bridge, bridgedDevices = l2All?.devices || [];
  updateLinkRates(paths);
  updateExitRates(current?.exit?.servers || []);
  renderNetworkMetrics();
  renderPies(routed, others, extra, bridgedDevices);
  const [health, headline] = overallHealth(paths);
  topoHead.replaceChildren();
  if (health === 'good') { topoHead.append('Everything is ', element('b', 'great', { class: 'good' }), '!'); } else topoHead.append(element('b', headline, { class: health }));
  const totalRx = paths.reduce((s, p) => s + (linkRates.get(p.slot)?.rxBps || 0), 0), totalTx = paths.reduce((s, p) => s + (linkRates.get(p.slot)?.txBps || 0), 0);
  topoStats.replaceChildren();
  const statRow = [[paths.filter(p => p.healthy).length + '/' + paths.length, 'Links up'], [routed.length, 'Routed devices'], [others.length + (extra ? '+' : ''), 'Other LAN devices']];
  if (bridgedDevices.length) statRow.push([bridgedDevices.length, 'Bridged (L2) devices']);
  statRow.push(['↓ ' + fmtBps(totalRx), 'Wire in (all links)'], ['↑ ' + fmtBps(totalTx), 'Wire out (all links)']);
  for (const [value, label] of statRow) {
    const box = element('div', null, { class: 'topo-stat' }); box.append(element('strong', String(value)), element('span', label)); topoStats.append(box);
  }

  topoMap.replaceChildren();
  const exit = current?.exit, standbys = (exit?.servers || []).filter(x => !x.active);
  const l2 = l2All, l2Devices = bridgedDevices.slice(0, 6);
  const rowGap = 72, rows = Math.max(paths.length, routed.length + others.length + (l2 ? 2 + l2Devices.length : 0), 2,
    standbys.length ? Math.ceil(2 + standbys.length * 1.6) : 0, l2 ? Math.ceil(2 + standbys.length * 1.6) + 1 : 0), height = 40 + rows * rowGap;
  topoMap.setAttribute('viewBox', '0 0 1060 ' + height);
  const midY = height / 2 - 24, serverX = 10, linkX = 215, pcX = 420, hubX = 625, devX = 850, w = 180, h = 46, dh = 62; // dh: routed-device nodes carry a third line (active ports)
  const onBackup = !!exit?.on_backup;
  const eh = 72, ey = midY + h / 2 - eh / 2, eg = eh + 12; // exit nodes carry rate and total lines, so they are taller
  const exitLines = x => {
    const r = exitRates.get(x.address) || {};
    const health = x.health_breaker ? x.health_breaker + ' · ' + Number(x.health_timeout_ms || 0).toFixed(0) + ' ms timeout' : 'health warming up';
    return ['↓ ' + fmtBps(r.rxBps) + '  ↑ ' + fmtBps(r.txBps), 'total ↓ ' + bytes(x.wire_rx_bytes) + '  ↑ ' + bytes(x.wire_tx_bytes), health];
  };
  const activeX = (exit?.servers || []).find(x => x.active) || { address: exit?.active || '', wire_rx_bytes: 0, wire_tx_bytes: 0 };
  const activeAddress = exit?.active || (current?.peer ? current.peer[0] : '');
  const server = liveNodeBox('exit:active', serverX, ey, w, eh, exit && standbys.length ? (onBackup ? 'Exit server · backup' : 'Exit server · primary') : 'Exit server', exitHostname(activeAddress), exit?.searching ? 'fair' : health === 'poor' ? 'poor' : onBackup ? 'fair' : 'good', null, false, exitLines(activeX));
  const pc = liveNodeBox('client:local', pcX, midY, w, h, 'This PC', 'GameBond client', 'good');
  topoMap.append(server, pc);
  // Standby exits hang under the active one, dashed: they carry nothing until the active exit stops answering.
  standbys.forEach((x, i) => {
    const y = ey + (i + 1) * eg, state = x.state === 'reachable' ? 'good' : x.state === 'unreachable' ? 'poor' : 'neutral';
    const label = x.state === 'reachable' ? 'standby · ' + Math.round(x.handshake_ms) + ' ms' : x.state === 'unreachable' ? 'standby · no answer' : 'standby · not probed';
    topoMap.append(mapLine(serverX + w / 2, (i ? y - eg + eh : ey + eh), serverX + w / 2, y, true), liveNodeBox('exit:standby:' + x.address, serverX, y, w, eh, 'Standby ' + exitHostname(x.address), label, state, null, false, exitLines(x)));
  });
  const linkY0 = midY + h / 2 - (paths.length - 1) * rowGap / 2 - h / 2;
  paths.forEach((p, i) => {
    const y = linkY0 + i * rowGap, r = linkRates.get(p.slot) || {};
    const fl = { down: r.rxBps || 0, up: r.txBps || 0 };
    topoMap.append(mapLine(serverX + w, midY + h / 2, linkX, y + h / 2, false, null, fl), mapLine(linkX + w, y + h / 2, pcX, midY + h / 2, false, null, fl));
    topoMap.append(liveNodeBox('path:' + p.slot, linkX, y, w, h, 'Link ' + (p.slot + 1) + (p.slot === 0 ? ' · Ethernet' : p.slot === 1 ? ' · Hotspot' : ''), '↓ ' + fmtBps(r.rxBps) + '  ↑ ' + fmtBps(r.txBps) + (p.rtt_ms != null ? '  ' + Math.round(p.rtt_ms) + ' ms' : ''), p.healthy ? 'good' : 'poor'));
  });
  // Devices sharing one physical adapter (an access point or switch on it) hang off one adapter node,
  // so the traffic through that adapter is visible as a whole. Other LAN devices attach to the PC.
  const groups = new Map();
  for (const d of routed) { const k = d.adapterMac || 'unknown'; if (!groups.has(k)) groups.set(k, []); groups.get(k).push(d); }
  const hubs = [...groups].map(([mac, devices]) => ({ key: 'adapter:' + mac, mac, devices, name: 'LAN adapter', up: devices.some(d => d.up),
    down: devices.reduce((a, d) => a + (d.usage?.down_bps || 0), 0), upBps: devices.reduce((a, d) => a + (d.usage?.up_bps || 0), 0) }));
  const all = [...hubs.flatMap(g => g.devices), ...others], devY0 = midY + h / 2 - (all.length - 1) * rowGap / 2 - h / 2;
  const placed = new Map(all.map((d, i) => [d.key, devY0 + i * rowGap]));
  for (const hub of hubs) {
    const ys = hub.devices.map(d => placed.get(d.key)), hy = (Math.min(...ys) + Math.max(...ys)) / 2;
    topoMap.append(mapLine(pcX + w, midY + h / 2, hubX, hy + h / 2, false, null, { down: hub.down, up: hub.upBps }));
    topoMap.append(liveNodeBox('adapter:' + hub.mac, hubX, hy, w, h, hub.name + ' · ' + hub.devices.length + (hub.devices.length === 1 ? ' device' : ' devices'), '↓ ' + fmtBps(hub.down) + '  ↑ ' + fmtBps(hub.upBps), hub.up ? 'good' : 'poor', () => { selectedDevice = hub.key; renderTopology(); }, selectedDevice === hub.key));
    for (const d of hub.devices) {
      const y = placed.get(d.key);
      topoMap.append(mapLine(hubX + w, hy + h / 2, devX, y + h / 2, false, null, { down: d.usage?.down_bps || 0, up: d.usage?.up_bps || 0 }));
      const sub = d.usage ? '↓ ' + fmtBps(d.usage.down_bps) + '  ↑ ' + fmtBps(d.usage.up_bps) : d.ip;
      const ap = activePorts(d.usage);
      // Taller than the other nodes but centred on the same row, so the connector still meets its middle.
      topoMap.append(liveNodeBox('device:' + d.key, devX, y - (dh - h) / 2, w, dh, d.name, sub, d.up && d.present !== false ? 'good' : 'poor', () => { selectedDevice = d.key; renderTopology(); }, selectedDevice === d.key, ap.line, ap.tip));
    }
  }
  for (const d of others) {
    const y = placed.get(d.key);
    topoMap.append(mapLine(pcX + w, midY + h / 2, devX, y + h / 2, true));
    topoMap.append(liveNodeBox('device:' + d.key, devX, y, w, h, d.name, d.ip + ' · not routed', 'idle', () => { selectedDevice = d.key; renderTopology(); }, selectedDevice === d.key));
  }
  if (extra) topoMap.append(svg('text', { class: 'tb-sub', x: devX, y: height - 8, 'font-size': 11 }, '+' + extra + ' more LAN devices (see the Devices tab)'));
  if (!all.length) topoMap.append(svg('text', { class: 'tb-sub', x: devX, y: midY + 28, 'font-size': 12 }, 'No devices seen yet'));

  // Layer 2 bridge: the adapter is carried as Ethernet frames through the bond to the exit's userspace gateway
  // (ARP, DHCP and every flow terminated there), so the devices keep their own MACs instead of a routed tunnel IP.
  // The bridge is part of this PC, so its frames leave the same way every other packet does — through the
  // bonded links to the exit server — never a line straight from the bridge to the gateway. The gateway node
  // just hangs off the exit server (dashed, like a standby) to show where that traffic is actually consumed.
  if (l2) {
    const r = l2Rates(l2), by = height - 62;
    const gwY = ey + eh + 12 + standbys.length * eg;
    topoMap.append(mapLine(serverX + w / 2, ey + eh, serverX + w / 2, gwY + h / 2, true));
    topoMap.append(liveNodeBox('exit:gateway', serverX, gwY, w, h, 'Exit gateway · 10.78.0.1', 'userspace gVisor · ARP, DHCP, NAT-free', l2.up ? 'good' : 'idle'));
    topoMap.append(mapLine(pcX + w, midY + h / 2, hubX, by + h / 2, false, null, { down: r.fromFps * 1000, up: r.toFps * 1000 }));
    topoMap.append(liveNodeBox('bridge:local', hubX, by, w, h, 'Layer 2 bridge · ' + (l2.name || 'adapter'), (l2.up ? 'up' : 'down') + ' · ' + (l2.fdb_size ?? 0) + ' MACs · ' + Math.round(r.fromFps) + '↓ ' + Math.round(r.toFps) + '↑ fps', l2.up ? 'good' : 'poor', null, false, null,
      'Frames to exit ' + (l2.to_tunnel ?? 0) + ', from exit ' + (l2.from_tunnel ?? 0) + ', flooded ' + (l2.flooded ?? 0) + ', dropped (storm/limit/blocked) ' + ((l2.drop_storm || 0) + (l2.drop_mac_limit || 0) + (l2.drop_blocked || 0))));
    if (l2Devices.length) {
      // Bridged devices are known to the switch only by MAC. The scanner's passive neighbor cache
      // only sees this host's own LAN segment, so it rarely has an entry for a device that is
      // actually beyond the tunnel; treat a resolved name as a bonus, not something the MAC can be
      // swapped out for, so the label stays useful even when (as is typical) nothing resolves.
      const l2Names = new Map((deviceInventory?.devices || []).map(x => [macKey(x.mac), x.hostname || x.vendor || '']));
      const devY0 = by - (l2Devices.length - 1) * (rowGap * 0.6) / 2;
      l2Devices.forEach((d, i) => {
        const y = devY0 + i * rowGap * 0.6, resolved = l2Names.get(macKey(d.mac));
        topoMap.append(mapLine(hubX + w, by + h / 2, devX, y + h / 2, false));
        topoMap.append(liveNodeBox('bridge-device:' + macKey(d.mac), devX, y, w, h, d.mac, (resolved ? resolved + ' · ' : '') + (d.age_secs ?? 0) + 's ago' + (d.vlan ? ' · VLAN ' + d.vlan : ''), 'good'));
      });
      if ((l2.devices || []).length > l2Devices.length) {
        topoMap.append(svg('text', { class: 'tb-sub', x: devX, y: devY0 + l2Devices.length * rowGap * 0.6 + 14, 'font-size': 11 }, '+' + ((l2.devices || []).length - l2Devices.length) + ' more bridged devices'));
      }
    } else {
      topoMap.append(mapLine(hubX + w, by + h / 2, devX, by + h / 2, true));
      topoMap.append(svg('text', { class: 'tb-sub', x: devX, y: by + h / 2 + 4, 'font-size': 11 }, 'No devices learned yet'));
    }
  }
  syncLiveEdges();
  const hub = hubs.find(g => g.key === selectedDevice);
  if (hub) renderAdapterCard(hub); else renderDeviceCard(all.find(d => d.key === selectedDevice));
}

function renderAdapterCard(hub) {
  topoCard.replaceChildren();
  const card = element('div', null, { class: 'dev-card' }), title = element('h2', 'LAN adapter');
  title.append(element('span', hub.up ? 'Online' : 'Offline', { class: 'chip ' + (hub.up ? 'good' : 'poor') }));
  card.append(title, element('p', 'Adapter MAC ' + hub.mac + ' · everything below reaches the tunnel through this adapter (for example an access point and the devices connected to it).', { class: 'sub' }));
  const peakDown = Math.max(1, ...hub.devices.map(d => d.usage?.peak_down_bps || 0)), peakUp = Math.max(1, ...hub.devices.map(d => d.usage?.peak_up_bps || 0));
  const gauges = element('div', null, { class: 'gauges' });
  gauges.append(gauge('Download', hub.down, Math.max(peakDown, hub.down), '#2fb800'), gauge('Upload', hub.upBps, Math.max(peakUp, hub.upBps), '#0f7ae5'));
  const now = element('div'); now.append(element('h3', 'Combined usage'), gauges);
  const table = element('table'); table.innerHTML = '<thead><tr><th>Device</th><th>IP</th><th>Down now</th><th>Up now</th><th>Total down</th><th>Total up</th><th>7 days</th><th>30 days</th></tr></thead>';
  const body = element('tbody'); table.append(body);
  for (const d of hub.devices) {
    const tr = element('tr'), u = d.usage;
    cell(tr, d.name); cell(tr, d.ip); cell(tr, u ? fmtBps(u.down_bps) : '—'); cell(tr, u ? fmtBps(u.up_bps) : '—'); cell(tr, u ? bytes(u.down_bytes) : '—'); cell(tr, u ? bytes(u.up_bytes) : '—');
    cell(tr, u?.usage_week ? bytes(u.usage_week.down + u.usage_week.up) : '—'); cell(tr, u?.usage_month ? bytes(u.usage_month.down + u.usage_month.up) : '—');
    tr.style.cursor = 'pointer'; tr.onclick = () => { selectedDevice = d.key; renderTopology(); }; body.append(tr);
  }
  const list = element('div'); list.append(element('h3', 'Devices behind it'), table);
  const grid = element('div', null, { class: 'dev-grid' }); grid.append(now, list); card.append(grid); topoCard.append(card);
}

// usageSection: how much data a device moved in the last day, week and month (rolling windows of hours), and a bar
// per day for the last 30 days. Hidden when the client does not report it yet.
function usageSection(u) {
  if (!u.usage_week || !u.usage_month || !u.usage_daily) return null;
  const box = element('div'); box.append(element('h3', 'Data usage'));
  const tiles = element('div', null, { class: 'usage-tiles' });
  for (const [label, w] of [['Last 24 hours', u.usage_day || { up: 0, down: 0 }], ['Last 7 days', u.usage_week], ['Last 30 days', u.usage_month]]) {
    const t = element('div', null, { class: 'usage-tile' });
    t.append(element('small', label), element('b', bytes((w.down || 0) + (w.up || 0))), element('span', '↓ ' + bytes(w.down || 0) + ' · ↑ ' + bytes(w.up || 0)));
    tiles.append(t);
  }
  box.append(tiles);
  const days = u.usage_daily, width = 620, height = 150, left = 44, bottom = 18, plot = height - bottom, usable = plot - 12, bw = (width - left - 4) / days.length;
  const top = Math.max(1e6, ...days.map(d => d[0] + d[1])) * 1.1;
  const s = svg('svg', { class: 'topo-svg', viewBox: '0 0 ' + width + ' ' + height, role: 'img', 'aria-label': 'Data used per day over the last 30 days' });
  s.style.maxWidth = '720px';
  for (let g = 0; g <= 3; g++) { const y = plot - g / 3 * usable; s.append(svg('line', { class: 'gl', x1: left, y1: y, x2: width - 4, y2: y }), svg('text', { class: 'tb-sub', x: left - 4, y: y + 3, 'font-size': 10, 'text-anchor': 'end' }, g ? bytes(top * g / 3) : '0')); }
  s.append(svg('text', { class: 'tb-sub', x: left, y: height - 4, 'font-size': 10 }, '-30 d'), svg('text', { class: 'tb-sub', x: width - 4, y: height - 4, 'font-size': 10, 'text-anchor': 'end' }, 'now'));
  days.forEach((d, k) => { // index 0 is the most recent 24 hours, so draw right to left
    const i = days.length - 1 - k, x = left + i * bw, hd = d[1] / top * usable, hu = d[0] / top * usable;
    if (hd >= 0.5) s.append(svg('rect', { x: x + 1, y: plot - hd, width: Math.max(1, bw - 2), height: hd, rx: 1.5, fill: '#2fb800', 'fill-opacity': 0.8 }));
    if (hu >= 0.5) s.append(svg('rect', { x: x + 1, y: plot - hd - hu, width: Math.max(1, bw - 2), height: hu, rx: 1.5, fill: '#0f7ae5', 'fill-opacity': 0.8 }));
    const hit = svg('rect', { x, y: 0, width: bw, height: plot, fill: 'transparent' });
    hit.append(svg('title', {}, (k === 0 ? 'Last 24 hours' : k === 1 ? '24 to 48 hours ago' : (k * 24) + ' to ' + ((k + 1) * 24) + ' hours ago') + '\n↓ ' + bytes(d[1]) + ' · ↑ ' + bytes(d[0]))); s.append(hit);
  });
  const lg = element('div', null, { class: 'tp-legend' });
  lg.innerHTML = '<span><i style="background:#2fb800;opacity:.8"></i>Download</span><span><i style="background:#0f7ae5;opacity:.8"></i>Upload</span>';
  box.append(s, lg, element('p', 'Counts what this device moved through the tunnel; each bar is one 24-hour block counted back from now. Totals are kept for 35 days.', { class: 'sub' }));
  return box;
}

const palette = ['#0f7ae5', '#2fb800', '#f5a300', '#9b59d0', '#e0201b', '#00a5b8', '#8c6d46', '#7b8089'];
function renderDeviceCard(d) {
  topoCard.replaceChildren();
  if (!d) { topoCard.append(element('p', 'Select a device on the map to see its details, current usage and history.', { class: 'sub' })); return; }
  const card = element('div', null, { class: 'dev-card' });
  const title = element('h2', d.name); title.append(element('span', d.up ? (d.routed ? 'Online · routed through GameBond' : 'Online · local network only') : 'Offline', { class: 'chip ' + (d.up ? 'good' : 'poor') }));
  card.append(title, element('p', d.ip + (d.mac ? ' · ' + d.mac : ''), { class: 'sub' }));
  const grid = element('div', null, { class: 'dev-grid' });

  const details = element('div'); details.append(element('h3', 'Device'));
  const table = element('table'), body = element('tbody'); table.append(body);
  const rows = d.routed
    ? [['Tunnel IP', d.ip], ['Device MAC', d.mac || 'not seen yet'], ['LAN adapter MAC', d.adapterMac || '—'], ['DHCP lease', d.leased ? 'Leased' : 'Not leased'], ['Last frame', d.silent != null ? Number(d.silent).toFixed(1) + ' s ago' : '—'], ['Problem', d.error || 'None'],
      ['First seen (usage)', d.usage ? deviceStamp(d.usage.first_seen) : '—'], ['Last traffic', d.usage ? ago(d.usage.last_seen) : '—']]
    : [['IP', d.ip], ['MAC', d.mac || '—'], ['Vendor', d.vendor || '—'], ['First seen', deviceStamp(d.first)], ['Last seen', deviceStamp(d.last)], ['Route', 'Local network (not through GameBond)']];
  for (const [k, v] of rows) { const tr = element('tr'); cell(tr, k); cell(tr, v); body.append(tr); }
  details.append(table); grid.append(details);

  if (!d.routed || !d.usage) {
    const note = element('div'); note.append(element('h3', 'Usage'),
      element('p', d.routed ? 'No traffic from this device has crossed the tunnel yet.' : 'Traffic for this device is not visible. It talks to the network directly and never passes through the GameBond tunnel, so there is nothing to measure. Only devices routed through a GameBond LAN adapter have usage data.', { class: 'notice' }));
    grid.append(note); card.append(grid); topoCard.append(card); return;
  }
  const u = d.usage, peakDown = Math.max(u.peak_down_bps || 0, u.down_bps || 0), peakUp = Math.max(u.peak_up_bps || 0, u.up_bps || 0);
  const now = element('div'); now.append(element('h3', 'Current usage'));
  const gauges = element('div', null, { class: 'gauges' });
  gauges.append(gauge('Download', u.down_bps, peakDown, '#2fb800'), gauge('Upload', u.up_bps, peakUp, '#0f7ae5'));
  now.append(gauges, element('p', 'Total ↓ ' + bytes(u.down_bytes) + ' · ↑ ' + bytes(u.up_bytes) + ' · ' + (Number(u.down_packets) + Number(u.up_packets)).toLocaleString() + ' packets', { class: 'sub' }));
  grid.append(now);

  const svcs = u.services || [], total = svcs.reduce((s, r) => s + r.up_bytes + r.down_bytes, 0) || 1;
  const apps = element('div'); apps.append(element('h3', 'Apps and services'), donut(svcs, total));
  svcs.slice(0, 8).forEach((r, i) => {
    const part = r.up_bytes + r.down_bytes, row = element('div', null, { class: 'svc-row' }), dot = element('i'), bar = element('div', null, { class: 'svc-bar' }), fill = element('div');
    dot.style.background = palette[i % palette.length]; fill.style.width = Math.max(2, part / total * 100) + '%'; fill.style.background = palette[i % palette.length]; bar.append(fill);
    row.append(dot, element('span', r.service), bar, element('span', bytes(part), { title: '↓ ' + bytes(r.down_bytes) + ' ↑ ' + bytes(r.up_bytes) })); apps.append(row);
  });
  grid.append(apps); card.append(grid);
  const usageBox = usageSection(u); if (usageBox) card.append(usageBox);

  const hist = element('div'); hist.append(element('h3', 'History'));
  const range = element('div', null, { class: 'range' });
  for (const [label, minutes] of [['1 h', 60], ['6 h', 360], ['24 h', 1440]]) {
    const b = element('button', label, { class: 'seg' + (usageRange === minutes ? ' active' : '') }); b.onclick = () => { usageRange = minutes; renderTopology(); }; range.append(b);
  }
  const panel = (name, dir) => {
    const p = element('div', null, { class: 'tp-panel' }), color = dir === 'down' ? '#2fb800' : '#0f7ae5', lg = element('div', null, { class: 'tp-legend' });
    lg.innerHTML = '<span><i style="background:' + color + ';opacity:.6"></i>Average</span><span><i style="background:none;border-top:1px dashed ' + color + ';height:0;vertical-align:3px"></i>Busiest minute</span><span><i style="background:#f5a300"></i>Latency (TCP handshake)</span>';
    p.append(element('h4', name), throughputChart(u.history || [], u.latency || [], usageRange, dir), lg); return p;
  };
  hist.append(range, panel('Download throughput & latency', 'down'), panel('Upload throughput & latency', 'up'),
    element('p', (u.latency || []).length ? 'Latency is the round trip from this device to the hosts it connects to, timed from TCP handshakes crossing the tunnel (median per minute).' : 'No TCP connections from this device were timed yet, so there is no latency line.', { class: 'sub' })); card.append(hist);

  const ports = element('div'); ports.append(element('h3', 'Ports (remote side) · ' + (u.active_ports || 0) + ' active'));
  const pt = element('table'); pt.innerHTML = '<thead><tr><th>Proto</th><th>Port</th><th>Service</th><th>Now</th><th>Down</th><th>Up</th><th>Packets</th></tr></thead>';
  const pb = element('tbody'); pt.append(pb);
  for (const r of (u.ports || []).slice(0, 12)) { const tr = element('tr'); cell(tr, String(r.proto).toUpperCase()); cell(tr, r.port || '—'); cell(tr, r.service || '—'); cell(tr, r.active ? 'active' : ago(r.last_seen)).className = r.active ? 'port-active' : ''; cell(tr, bytes(r.down_bytes)); cell(tr, bytes(r.up_bytes)); cell(tr, (r.packets || 0).toLocaleString()); pb.append(tr); }
  ports.append(pt);
  const hosts = element('p', 'Top remote hosts: ' + ((u.remotes || []).slice(0, 5).map(r => r.ip + ' (' + bytes(r.bytes) + ')').join(', ') || '—'), { class: 'sub' });
  ports.append(hosts, element('p', 'Services are inferred from the remote port; the port table counts traffic since the client started, history persists for 24 hours.', { class: 'sub' }));
  card.append(ports); topoCard.append(card);
}

function gauge(label, bps, peak, color) {
  const box = element('div', null, { class: 'gauge' }), s = svg('svg', { width: 84, height: 84, viewBox: '0 0 84 84', role: 'img', 'aria-label': label + ' ' + fmtBps(bps) });
  const id = 'gc' + label, scale = Math.max(peak, 1e6), level = Math.min(1, (bps || 0) / scale), h = 76 * level;
  const clip = svg('clipPath', { id }); clip.append(svg('circle', { cx: 42, cy: 42, r: 38 }));
  s.append(clip, svg('circle', { class: 'g-bg', cx: 42, cy: 42, r: 38, 'stroke-width': 2 }),
    svg('rect', { x: 4, y: 80 - h, width: 76, height: h, fill: color, 'fill-opacity': 0.35, 'clip-path': 'url(#' + id + ')' }));
  box.append(s, element('b', fmtBps(bps)), element('span', label + ' · peak ' + fmtBps(peak)));
  return box;
}
function donut(rows, total) {
  const s = svg('svg', { width: 110, height: 110, viewBox: '0 0 110 110', role: 'img', 'aria-label': 'Traffic by service' }), r = 40, c = 2 * Math.PI * r;
  s.append(svg('circle', { class: 'd-track', cx: 55, cy: 55, r, fill: 'none', 'stroke-width': 16 }));
  let offset = 0;
  rows.slice(0, 8).forEach((row, i) => {
    const len = (row.up_bytes + row.down_bytes) / total * c;
    s.append(svg('circle', { cx: 55, cy: 55, r, fill: 'none', stroke: palette[i % palette.length], 'stroke-width': 16, 'stroke-dasharray': len + ' ' + (c - len), 'stroke-dashoffset': -offset, transform: 'rotate(-90 55 55)' }));
    offset += len;
  });
  return s;
}
// Minute triples [unix, up, down] -> bars of Mbps over the last `minutes`.

setInterval(() => { if (scannerSub === 'topology' && $('scanner').classList.contains('active')) renderTopology(); }, 1000);
const selectTabBeforeTopology = selectTab;
selectTab = function(id) { selectTabBeforeTopology(id); if (id === 'scanner' && scannerSub === 'topology') renderTopology(); };
