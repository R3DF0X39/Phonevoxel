'use strict';
// ─────────────────────────────────────────────────────────────────────────────
// WebcamVoxel frontend
// Leaflet map (camera placement + ground-plane selection) +
// Three.js 3D view (voxels + satellite ground plane + camera frustums) +
// WebSocket dashboard feed
// ─────────────────────────────────────────────────────────────────────────────

// ── State ─────────────────────────────────────────────────────────────────────
const state = {
  cameras:      [],   // WebcamCameraEntry objects fetched from server
  gridMeta:     null,
  lastUpdate:   null,
  voxThreshold: 0.05,
  showVoxels:   true,
  showCams:     true,
  showRays:     true,
  showGround:   true,
  groundBounds: null, // { south, west, north, east } — lat/lon
};

// Phone clients seen in the latest WS update (indexed by id)
const phoneClientMap  = new Map(); // id → ClientStatus
const phoneMarkers    = new Map(); // id → Leaflet marker
const phoneFeedIDs    = new Set(); // ids that have feed tiles in the strip

// placement state machine
const place = {
  mode:    'idle',  // 'idle' | 'origin' | 'bearing'
  lat:     0,
  lon:     0,
  azimuth: 0,
  pendingCamConfig: null, // partially-filled config from modal
  editingId: null,        // when editing an existing camera
};

// ── WebSocket ─────────────────────────────────────────────────────────────────
const WS_URL = (location.protocol === 'https:' ? 'wss://' : 'ws://') +
    location.host + '/ws/dashboard';

let ws = null;

function openWS() {
  ws = new WebSocket(WS_URL);
  ws.onopen  = () => setBadge('ws', 'on', 'live');
  ws.onclose = () => { setBadge('ws', 'err', 'disconnected'); setTimeout(openWS, 3000); };
  ws.onerror = () => ws.close();
  ws.onmessage = e => {
    try {
      const data = JSON.parse(e.data);
      state.lastUpdate = data;
      state.gridMeta   = data.grid_meta;
      updateTargetList(data.targets || []);
      update3D(data.sparse_voxels || [], data.targets || [], data.clients || [], data.grid_meta);
      updateCamFPS(data.clients || []);
      updatePhoneClients(data.clients || []);
    } catch(err) { console.error('WS parse', err); }
  };
}
openWS();

// ── Badges / toast ────────────────────────────────────────────────────────────
function setBadge(key, dotCls, text) {
  const dot  = document.getElementById('dot-' + key);
  const span = document.getElementById('badge-' + key);
  dot.className = 'dot ' + (dotCls || '');
  if (span) span.textContent = text;
}

let toastTimer = null;
function toast(msg, ms = 2500) {
  const el = document.getElementById('wv-toast');
  el.textContent = msg;
  el.classList.remove('hidden');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add('hidden'), ms);
}

// ── Leaflet map ───────────────────────────────────────────────────────────────
const map = L.map('wv-map', {
  center: [51.505, -0.09],
  zoom: 16,
  zoomControl: true,
});

// Satellite layer (Esri WorldImagery)
L.tileLayer(
  'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
  { attribution: 'Esri WorldImagery', maxZoom: 19 }
).addTo(map);

// ── Camera markers on map ─────────────────────────────────────────────────────
const markerMap = new Map(); // camId → { marker, bearingLayer }

function camIcon(selected) {
  return L.divIcon({
    className: '',
    html: `<div class="cam-marker-icon${selected ? ' selected' : ''}">📷</div>`,
    iconSize: [28, 28],
    iconAnchor: [14, 14],
  });
}

function addCameraMarker(cam) {
  const marker = L.marker([cam.lat, cam.lon], {
    icon: camIcon(false),
    draggable: true,
    title: cam.name,
  }).addTo(map);

  marker.on('click', () => selectCamera(cam.id));
  marker.on('dragend', e => {
    const ll = e.target.getLatLng();
    cam.lat = ll.lat;
    cam.lon = ll.lng;
    saveCamera(cam);
    drawBearingLine(cam);
  });

  const bearingLayer = drawBearingLine(cam);
  markerMap.set(cam.id, { marker, bearingLayer });
}

function drawBearingLine(cam) {
  const entry = markerMap.get(cam.id);
  if (entry && entry.bearingLayer) {
    map.removeLayer(entry.bearingLayer);
  }

  // 200m bearing line from camera position
  const dist = 150; // metres
  const az = cam.azimuth * Math.PI / 180;
  const dLat = (dist * Math.cos(az)) / 111320;
  const dLon = (dist * Math.sin(az)) / (111320 * Math.cos(cam.lat * Math.PI / 180));
  const tip = [cam.lat + dLat, cam.lon + dLon];

  const line = L.polyline([[cam.lat, cam.lon], tip], {
    color: '#f07830',
    weight: 2,
    opacity: 0.8,
    dashArray: '6 4',
    className: 'cam-bearing-line',
  }).addTo(map);

  if (entry) entry.bearingLayer = line;
  return line;
}

function removeCameraMarker(camId) {
  const entry = markerMap.get(camId);
  if (!entry) return;
  map.removeLayer(entry.marker);
  if (entry.bearingLayer) map.removeLayer(entry.bearingLayer);
  markerMap.delete(camId);
}

function selectCamera(camId) {
  markerMap.forEach((entry, id) => {
    entry.marker.setIcon(camIcon(id === camId));
  });
  renderCamList();
}

// ── Camera placement flow ─────────────────────────────────────────────────────
const mapHint   = document.getElementById('wv-map-hint');
const aimPopout = document.getElementById('wv-aim-popout');

function setPlaceMode(mode) {
  place.mode = mode;

  const btnPlace = document.getElementById('btn-place-camera');
  btnPlace.classList.toggle('active', mode !== 'idle');

  if (mode === 'origin') {
    mapHint.textContent = 'Click on the map to drop the camera origin';
    mapHint.classList.remove('hidden');
    map.getContainer().style.cursor = 'crosshair';
  } else if (mode === 'bearing') {
    mapHint.textContent = 'Click a second point to set the azimuth direction';
    mapHint.classList.remove('hidden');
    map.getContainer().style.cursor = 'crosshair';
  } else {
    mapHint.classList.add('hidden');
    map.getContainer().style.cursor = '';
  }
}

map.on('click', e => {
  if (place.mode === 'origin') {
    place.lat = e.latlng.lat;
    place.lon = e.latlng.lng;
    // Show a temporary preview marker
    const preview = L.circleMarker([place.lat, place.lon], {
      radius: 8, color: '#f07830', fillColor: '#f07830', fillOpacity: 0.7,
    }).addTo(map);
    place.previewMarker = preview;
    setPlaceMode('bearing');

  } else if (place.mode === 'bearing') {
    const az = computeAzimuth({ lat: place.lat, lng: place.lon }, e.latlng);
    place.azimuth = az;

    if (place.previewMarker) {
      map.removeLayer(place.previewMarker);
      place.previewMarker = null;
    }

    // Show elevation popout near the click
    showAimPopout(e.containerPoint, az);
    setPlaceMode('idle');

  } else if (groundSelectState.active) {
    handleGroundSelectClick(e.latlng);
  }
});

map.on('mousemove', e => {
  if (place.mode === 'bearing') {
    // Update the pending bearing preview line
    const az = computeAzimuth({ lat: place.lat, lng: place.lon }, e.latlng);
    place.azimuth = az;
    document.getElementById('aim-az').value = az;
    document.getElementById('aim-az-val').textContent = az.toFixed(1);
  }
});

function computeAzimuth(from, to) {
  const dLon = (to.lng - from.lng) * Math.PI / 180;
  const lat1 = from.lat * Math.PI / 180;
  const lat2 = to.lat  * Math.PI / 180;
  const y = Math.sin(dLon) * Math.cos(lat2);
  const x = Math.cos(lat1) * Math.sin(lat2) - Math.sin(lat1) * Math.cos(lat2) * Math.cos(dLon);
  return ((Math.atan2(y, x) * 180 / Math.PI) + 360) % 360;
}

// ── Aim popout ────────────────────────────────────────────────────────────────
function showAimPopout(point, initialAz) {
  aimPopout.style.left = (point.x + 16) + 'px';
  aimPopout.style.top  = (point.y - 40) + 'px';
  aimPopout.classList.remove('hidden');

  document.getElementById('aim-az').value       = initialAz.toFixed(1);
  document.getElementById('aim-az-val').textContent = initialAz.toFixed(1);
  document.getElementById('aim-el').value       = 0;
  document.getElementById('aim-el-val').textContent = '0.0';
  document.getElementById('aim-el-num').value   = 0;
  document.getElementById('aim-roll').value     = 0;
  document.getElementById('aim-roll-val').textContent = '0';
}

function hideAimPopout() {
  aimPopout.classList.add('hidden');
  document.getElementById('aim-roll-section').classList.remove('visible');
}

// Sync aim sliders ↔ number inputs
['az', 'el', 'roll'].forEach(k => {
  const slider = document.getElementById('aim-' + k);
  const numIn  = document.getElementById('aim-' + k + '-num');
  const valEl  = document.getElementById('aim-' + k + '-val');
  slider.addEventListener('input', () => {
    const v = parseFloat(slider.value);
    if (valEl) valEl.textContent = v.toFixed(k === 'roll' ? 0 : 1);
    if (numIn) numIn.value = v;
  });
  if (numIn) {
    numIn.addEventListener('input', () => {
      slider.value = numIn.value;
      if (valEl) valEl.textContent = parseFloat(numIn.value).toFixed(1);
    });
  }
});

document.getElementById('toggle-roll').addEventListener('click', () => {
  document.getElementById('aim-roll-section').classList.toggle('visible');
});

document.getElementById('btn-aim-cancel').addEventListener('click', () => {
  hideAimPopout();
  setPlaceMode('idle');
});

document.getElementById('btn-aim-save').addEventListener('click', () => {
  const az   = parseFloat(document.getElementById('aim-az').value);
  const el   = parseFloat(document.getElementById('aim-el').value);
  const roll = parseFloat(document.getElementById('aim-roll').value);
  hideAimPopout();
  finaliseCameraPlacement(az, el, roll);
});

async function finaliseCameraPlacement(azimuth, elevation, roll) {
  if (place.editingId) {
    // Update existing camera pose
    const cam = state.cameras.find(c => c.id === place.editingId);
    if (cam) {
      cam.azimuth   = azimuth;
      cam.elevation = elevation;
      cam.roll      = roll;
      cam.lat       = place.lat;
      cam.lon       = place.lon;
      await saveCamera(cam);
      drawBearingLine(cam);
      renderCamList();
      toast(`Camera "${cam.name}" updated.`);
    }
    place.editingId = null;
  } else {
    // New camera — show config modal to fill in device/FOV/etc.
    place.pendingCamConfig = { azimuth, elevation, roll, lat: place.lat, lon: place.lon };
    openCameraModal(null);
  }
}

// ── Camera config modal ───────────────────────────────────────────────────────
const modalCamera   = document.getElementById('modal-camera');
const btnModalSave  = document.getElementById('btn-modal-save');
const btnModalCancel = document.getElementById('btn-modal-cancel');

document.getElementById('cam-cfg-preset').addEventListener('change', e => {
  if (e.target.value) {
    document.getElementById('cam-cfg-hfov').value = e.target.value;
  }
});

function openCameraModal(existingCam) {
  if (existingCam) {
    document.getElementById('modal-camera-title').textContent = 'Edit Camera';
    document.getElementById('cam-cfg-name').value   = existingCam.name;
    document.getElementById('cam-cfg-device').value = existingCam.device;
    document.getElementById('cam-cfg-width').value  = existingCam.width;
    document.getElementById('cam-cfg-height').value = existingCam.height;
    document.getElementById('cam-cfg-fps').value    = existingCam.fps;
    document.getElementById('cam-cfg-hfov').value   = existingCam.hfov;
    document.getElementById('cam-cfg-alt').value    = existingCam.alt_meters || 0;
    modalCamera._editingId = existingCam.id;
  } else {
    document.getElementById('modal-camera-title').textContent = 'Configure New Camera';
    document.getElementById('cam-cfg-name').value   = 'Camera ' + (state.cameras.length + 1);
    document.getElementById('cam-cfg-device').value = '/dev/video' + state.cameras.length;
    document.getElementById('cam-cfg-width').value  = 640;
    document.getElementById('cam-cfg-height').value = 480;
    document.getElementById('cam-cfg-fps').value    = 30;
    document.getElementById('cam-cfg-hfov').value   = 70;
    document.getElementById('cam-cfg-alt').value    = 0;
    modalCamera._editingId = null;
  }
  modalCamera.classList.remove('hidden');
}

btnModalCancel.addEventListener('click', () => {
  modalCamera.classList.add('hidden');
  place.pendingCamConfig = null;
  modalCamera._editingId = null;
});

btnModalSave.addEventListener('click', async () => {
  const name   = document.getElementById('cam-cfg-name').value.trim() || 'Camera';
  const device = document.getElementById('cam-cfg-device').value.trim();
  const width  = parseInt(document.getElementById('cam-cfg-width').value)  || 640;
  const height = parseInt(document.getElementById('cam-cfg-height').value) || 480;
  const fps    = parseInt(document.getElementById('cam-cfg-fps').value)    || 30;
  const hfov   = parseFloat(document.getElementById('cam-cfg-hfov').value) || 70;
  const alt    = parseFloat(document.getElementById('cam-cfg-alt').value)  || 0;

  if (!device) { toast('Device path is required.'); return; }
  modalCamera.classList.add('hidden');

  if (modalCamera._editingId) {
    // Update
    const cam = state.cameras.find(c => c.id === modalCamera._editingId);
    if (cam) {
      cam.name = name; cam.device = device; cam.width = width;
      cam.height = height; cam.fps = fps; cam.hfov = hfov; cam.alt_meters = alt;
      await saveCamera(cam);
      toast(`Camera "${name}" saved.`);
    }
  } else {
    // Create new
    const pending = place.pendingCamConfig || {};
    const newCam = {
      name, device, width, height, fps, hfov,
      alt_meters: alt,
      lat:       pending.lat       || 0,
      lon:       pending.lon       || 0,
      azimuth:   pending.azimuth   || 0,
      elevation: pending.elevation || 0,
      roll:      pending.roll      || 0,
    };
    await createCamera(newCam);
  }
  place.pendingCamConfig = null;
  modalCamera._editingId = null;
});

// ── Camera CRUD API ───────────────────────────────────────────────────────────
async function loadCameras() {
  try {
    const r = await fetch('/api/webcam/cameras');
    const cams = await r.json();
    state.cameras = cams || [];
    state.cameras.forEach(c => addCameraMarker(c));
    renderCamList();
    updateCamBadge();
  } catch(e) { console.error('loadCameras', e); }
}

async function createCamera(cam) {
  try {
    const r = await fetch('/api/webcam/cameras', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cam),
    });
    if (!r.ok) {
      const msg = await r.text();
      toast('Error: ' + msg, 4000);
      return;
    }
    const saved = await r.json();
    state.cameras.push(saved);
    addCameraMarker(saved);
    renderCamList();
    updateCamBadge();
    addFeedTiles(saved);
    toast(`Camera "${saved.name}" started!`);
  } catch(e) { toast('Network error: ' + e.message, 4000); }
}

async function saveCamera(cam) {
  try {
    const r = await fetch('/api/webcam/cameras/' + cam.id, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(cam),
    });
    if (!r.ok) { toast('Save failed: ' + await r.text(), 4000); }
  } catch(e) { toast('Network error: ' + e.message, 4000); }
}

async function deleteCamera(id) {
  if (!confirm('Delete this camera?')) return;
  try {
    await fetch('/api/webcam/cameras/' + id, { method: 'DELETE' });
    state.cameras = state.cameras.filter(c => c.id !== id);
    removeCameraMarker(id);
    removeFeedTiles(id);
    renderCamList();
    updateCamBadge();
    toast('Camera removed.');
  } catch(e) { toast('Delete error: ' + e.message, 4000); }
}

// ── Camera list render ────────────────────────────────────────────────────────
function renderCamList() {
  const el = document.getElementById('cam-list');
  if (!state.cameras.length) {
    el.innerHTML = '<div class="none-msg">No cameras configured.</div>';
    return;
  }
  el.innerHTML = state.cameras.map(cam => {
    const fps = getCamFPS(cam.id);
    return `
      <div class="cam-card" id="camcard-${cam.id}">
        <div class="cam-card-header">
          <div class="cam-dot ${fps > 0 ? 'live' : ''}"></div>
          <span class="cam-name">${esc(cam.name)}</span>
          ${fps > 0 ? `<span class="cam-fps">${fps.toFixed(1)}fps</span>` : ''}
        </div>
        <div class="cam-meta">
          <span>${esc(cam.device)}</span>
          <span>${cam.width}×${cam.height}@${cam.fps}fps</span>
          <span>Az ${cam.azimuth.toFixed(1)}° El ${cam.elevation.toFixed(1)}°</span>
          <span>FOV ${cam.hfov}°</span>
        </div>
        <div class="cam-actions">
          <button class="wv-btn sm" onclick="editCameraPose('${cam.id}')">Repoint</button>
          <button class="wv-btn sm" onclick="openCameraModal(state.cameras.find(c=>c.id==='${cam.id}'))">Edit</button>
          <button class="wv-btn sm danger" onclick="deleteCamera('${cam.id}')">✕</button>
        </div>
      </div>`;
  }).join('');
}

const camFPSMap = {};
function updateCamFPS(clients) {
  clients.forEach(c => { camFPSMap[c.id] = c.fps; });
}
function getCamFPS(id) { return camFPSMap[id] || 0; }

function updateCamBadge() {
  setBadge('cams', state.cameras.length > 0 ? 'on' : '',
    state.cameras.length + ' camera' + (state.cameras.length !== 1 ? 's' : ''));
}

function editCameraPose(id) {
  const cam = state.cameras.find(c => c.id === id);
  if (!cam) return;
  place.editingId = id;
  place.lat = cam.lat;
  place.lon = cam.lon;
  place.azimuth = cam.azimuth;
  setPlaceMode('origin');
  toast('Click on map to set new camera position');
}

// ── Bottom feed strip ─────────────────────────────────────────────────────────
function addFeedTiles(cam) {
  const feeds = document.getElementById('wv-feeds');

  const group = document.createElement('div');
  group.className = 'feed-group';
  group.id = 'feeds-' + cam.id;
  group.innerHTML = `
    <div class="feed-group-label">${esc(cam.name)}</div>
    <div style="display:flex;gap:2px">
      <div class="feed-tile raw" style="height:72px">
        <img src="/webcam/feed/${cam.id}" alt="raw">
        <div class="feed-tile-label">Raw</div>
      </div>
      <div class="feed-tile motion" style="height:72px">
        <img src="/webcam/motion/${cam.id}" alt="motion">
        <div class="feed-tile-label">Motion</div>
      </div>
    </div>`;
  feeds.appendChild(group);
}

function removeFeedTiles(camId) {
  const el = document.getElementById('feeds-' + camId);
  if (el) el.remove();
}

function initFeeds() {
  state.cameras.forEach(addFeedTiles);
}

// ── Ground-plane selection on map ─────────────────────────────────────────────
const groundSelectState = { active: false, firstClick: null, rect: null };
let groundRectLayer = null;

document.getElementById('btn-select-ground').addEventListener('click', () => {
  groundSelectState.active = !groundSelectState.active;
  groundSelectState.firstClick = null;
  const btn = document.getElementById('btn-select-ground');
  btn.classList.toggle('active', groundSelectState.active);
  if (groundSelectState.active) {
    mapHint.textContent = 'Click two corners to define the 3D ground plane area';
    mapHint.classList.remove('hidden');
    map.getContainer().style.cursor = 'crosshair';
  } else {
    mapHint.classList.add('hidden');
    map.getContainer().style.cursor = '';
  }
});

function handleGroundSelectClick(latlng) {
  if (!groundSelectState.firstClick) {
    groundSelectState.firstClick = latlng;
    if (groundRectLayer) { map.removeLayer(groundRectLayer); groundRectLayer = null; }
  } else {
    const a = groundSelectState.firstClick;
    const b = latlng;
    const bounds = {
      south: Math.min(a.lat, b.lat),
      north: Math.max(a.lat, b.lat),
      west:  Math.min(a.lng, b.lng),
      east:  Math.max(a.lng, b.lng),
    };
    state.groundBounds = bounds;

    if (groundRectLayer) map.removeLayer(groundRectLayer);
    groundRectLayer = L.rectangle(
      [[bounds.south, bounds.west], [bounds.north, bounds.east]],
      { color: '#3b9eff', weight: 2, fillOpacity: 0.08 }
    ).addTo(map);

    groundSelectState.active = false;
    groundSelectState.firstClick = null;
    document.getElementById('btn-select-ground').classList.remove('active');
    mapHint.classList.add('hidden');
    map.getContainer().style.cursor = '';

    // Set ENU origin to center of selection
    const centerLat = (bounds.south + bounds.north) / 2;
    const centerLon = (bounds.west  + bounds.east)  / 2;
    fetch('/api/webcam/origin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ lat: centerLat, lon: centerLon, alt: 0 }),
    }).then(() => {
      setBadge('origin', 'on', `origin: ${centerLat.toFixed(4)}, ${centerLon.toFixed(4)}`);
      loadGroundPlaneTexture(bounds);
      toast('Ground plane set. ENU origin updated.');
    }).catch(e => toast('Origin set failed: ' + e.message));
  }
}

// ── Three.js 3D scene ─────────────────────────────────────────────────────────
const wrap3d   = document.getElementById('wv-3d-canvas-wrap');
const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(window.devicePixelRatio);
renderer.setClearColor(0x05060e);
wrap3d.appendChild(renderer.domElement);

const scene  = new THREE.Scene();
const cam3d  = new THREE.PerspectiveCamera(50, 1, 0.1, 10000);

// Orbit controls (manual)
let isDragging = false, lastMX = 0, lastMY = 0;
let theta = 0, phi = Math.PI / 3.5, radius3d = 250;

function updateCamera3D() {
  cam3d.position.set(
    radius3d * Math.sin(phi) * Math.sin(theta),
    radius3d * Math.cos(phi),
    radius3d * Math.sin(phi) * Math.cos(theta),
  );
  cam3d.lookAt(0, 0, 0);
}
updateCamera3D();

renderer.domElement.addEventListener('mousedown', e => {
  isDragging = true; lastMX = e.clientX; lastMY = e.clientY;
});
window.addEventListener('mouseup', () => { isDragging = false; });
window.addEventListener('mousemove', e => {
  if (!isDragging) return;
  const dx = e.clientX - lastMX, dy = e.clientY - lastMY;
  lastMX = e.clientX; lastMY = e.clientY;
  theta += dx * 0.005;
  phi = Math.max(0.05, Math.min(Math.PI - 0.05, phi + dy * 0.005));
  updateCamera3D();
});
renderer.domElement.addEventListener('wheel', e => {
  radius3d = Math.max(10, Math.min(5000, radius3d + e.deltaY * 0.4));
  updateCamera3D();
}, { passive: true });

document.getElementById('btn-reset-view').addEventListener('click', () => {
  theta = 0; phi = Math.PI / 3.5; radius3d = 250;
  updateCamera3D();
});

// Axes helper (X=East, Y=Up, Z=-North in Three.js convention matching dashboard.js)
scene.add(new THREE.AxesHelper(20));

// ── Voxel instanced mesh ──────────────────────────────────────────────────────
const MAX_VOXELS = 60000;
const boxGeo  = new THREE.BoxGeometry(1, 1, 1);
const boxMat  = new THREE.MeshBasicMaterial({ vertexColors: true });
const voxMesh = new THREE.InstancedMesh(boxGeo, boxMat, MAX_VOXELS);
voxMesh.count = 0;
scene.add(voxMesh);

// Target spheres
const targetPool = [];
const sphGeo = new THREE.SphereGeometry(1.5, 8, 8);
const sphMat = new THREE.MeshBasicMaterial({ color: 0xff4040, wireframe: true });
for (let i = 0; i < 32; i++) {
  const m = new THREE.Mesh(sphGeo, sphMat);
  m.visible = false;
  scene.add(m);
  targetPool.push(m);
}

// Camera frustum group
const camGroup = new THREE.Group();
scene.add(camGroup);

// Ground plane
const groundPlane = new THREE.Mesh(
  new THREE.PlaneGeometry(1, 1),
  new THREE.MeshBasicMaterial({ side: THREE.DoubleSide, transparent: true, opacity: 0.85 })
);
groundPlane.rotation.x = -Math.PI / 2;
groundPlane.visible = false;
scene.add(groundPlane);

// Grid lines on ground
const gridHelper = new THREE.GridHelper(200, 20, 0x1a2a40, 0x0d1a28);
gridHelper.position.y = -0.01;
scene.add(gridHelper);

const dummy3d = new THREE.Object3D();
const colorObj3d = new THREE.Color();

function valueToColor3D(v) {
  const t = Math.min(v, 1);
  colorObj3d.setHSL((1 - t) * 0.66, 1, 0.5);
  return colorObj3d.clone();
}

function update3D(voxels, targets, clients, meta) {
  if (!meta) return;

  // Voxels
  if (state.showVoxels) {
    const count = Math.min(voxels.length, MAX_VOXELS);
    const res = meta.resolution;
    let shown = 0;
    for (let i = 0; i < count; i++) {
      const v = voxels[i];
      if (v.v < state.voxThreshold) continue;
      const e = meta.min_east  + (v.ix + 0.5) * res;
      const n = meta.min_north + (v.iy + 0.5) * res;
      const u = meta.min_up    + (v.iz + 0.5) * res;
      dummy3d.position.set(e, u, -n); // Three.js: X=E, Y=U, Z=-N
      dummy3d.scale.setScalar(res * 0.9);
      dummy3d.updateMatrix();
      voxMesh.setMatrixAt(shown, dummy3d.matrix);
      voxMesh.setColorAt(shown, valueToColor3D(v.v));
      shown++;
    }
    voxMesh.count = shown;
    voxMesh.instanceMatrix.needsUpdate = true;
    if (voxMesh.instanceColor) voxMesh.instanceColor.needsUpdate = true;
  } else {
    voxMesh.count = 0;
    voxMesh.instanceMatrix.needsUpdate = true;
  }

  // Targets
  targetPool.forEach((m, i) => {
    if (state.showVoxels && i < targets.length) {
      const t = targets[i];
      m.position.set(t.position[0], t.position[2], -t.position[1]);
      m.visible = true;
    } else {
      m.visible = false;
    }
  });

  // Update camera frustums from state.cameras (poses known from config)
  updateCamFrustums(meta);

  // Update grid helper size to match meta
  gridHelper.scale.setScalar(1);
  gridHelper.position.set(
    (meta.min_east + meta.max_east) / 2,
    meta.min_up - 0.01,
    -(meta.min_north + meta.max_north) / 2,
  );
}

function updateCamFrustums(meta) {
  camGroup.clear();
  if (!state.showCams) return;

  // ── Phone clients (ENU position already computed server-side) ────────────
  phoneClientMap.forEach(c => {
    if (!meta) return;
    const posE = c.east  || 0;
    const posN = c.north || 0;
    const posU = c.up    || 0;

    // Phone box (slightly different colour — blue)
    const box = new THREE.Mesh(
      new THREE.BoxGeometry(0.8, 0.4, 1.2),
      new THREE.MeshBasicMaterial({ color: 0x3b9eff, wireframe: true })
    );
    box.position.set(posE, posU, -posN);
    camGroup.add(box);

    if (state.showRays) {
      const az = c.heading * Math.PI / 180;
      // Phones are horizontal (elevation ≈ 0); use heading as azimuth
      const pts = [
        new THREE.Vector3(posE, posU, -posN),
        new THREE.Vector3(
          posE + Math.sin(az) * 50,
          posU,
          -(posN + Math.cos(az) * 50),
        ),
      ];
      const ray = new THREE.Line(
        new THREE.BufferGeometry().setFromPoints(pts),
        new THREE.LineBasicMaterial({ color: 0x3b9eff, opacity: 0.5, transparent: true })
      );
      camGroup.add(ray);
    }
  });

  // ── Webcam clients (pose from local config) ──────────────────────────────
  // Build simple camera icons + bearing ray for each webcam
  state.cameras.forEach(cam => {
    // We don't know ENU position without knowing the origin, but we can
    // show a symbolic camera if origin is set (geoConv on server side).
    // For the 3D view we use the relative ENU position from the voxel grid center.
    // Since we don't have ENU coords client-side, we approximate:
    // lat/lon → rough offset from grid meta center using 111320 m/deg.
    if (!meta) return;
    const originLat = parseFloat(document.getElementById('badge-origin').textContent.split(':')[1]) || cam.lat;
    const dLat = (cam.lat - originLat) * 111320;
    const dLon = (cam.lon - originLat) * 111320 * Math.cos(cam.lat * Math.PI / 180);
    const posE = dLon, posN = dLat, posU = cam.alt_meters || 0;

    // Camera box
    const box = new THREE.Mesh(
      new THREE.BoxGeometry(1.5, 1, 2),
      new THREE.MeshBasicMaterial({ color: 0xf07830, wireframe: true })
    );
    box.position.set(posE, posU, -posN);
    camGroup.add(box);

    if (state.showRays) {
      // Forward ray (50m)
      const az = cam.azimuth * Math.PI / 180;
      const el = cam.elevation * Math.PI / 180;
      const cosEl = Math.cos(el);
      const fwdE =  Math.sin(az) * cosEl;
      const fwdN =  Math.cos(az) * cosEl;
      const fwdU =  Math.sin(el);
      const len = 60;
      const pts = [
        new THREE.Vector3(posE, posU, -posN),
        new THREE.Vector3(posE + fwdE * len, posU + fwdU * len, -(posN + fwdN * len)),
      ];
      const rayGeo = new THREE.BufferGeometry().setFromPoints(pts);
      const ray = new THREE.Line(rayGeo,
        new THREE.LineBasicMaterial({ color: 0xf07830, opacity: 0.6, transparent: true }));
      camGroup.add(ray);
    }
  });
}

// ── Ground plane satellite texture ───────────────────────────────────────────
async function loadGroundPlaneTexture(bounds) {
  if (!bounds) return;

  // Choose appropriate zoom level (target ~4 tiles)
  const latSpan = bounds.north - bounds.south;
  const lonSpan = bounds.east  - bounds.west;
  const zoom = chooseTileZoom(latSpan, lonSpan);

  // Get tile range
  const { x: x0, y: y0 } = latLonToTile(bounds.north, bounds.west, zoom);
  const { x: x1, y: y1 } = latLonToTile(bounds.south, bounds.east, zoom);

  const nx = x1 - x0 + 1;
  const ny = y1 - y0 + 1;
  const TILE_SIZE = 256;
  const canvas = document.createElement('canvas');
  canvas.width  = nx * TILE_SIZE;
  canvas.height = ny * TILE_SIZE;
  const ctx = canvas.getContext('2d');

  const fetches = [];
  for (let ty = y0; ty <= y1; ty++) {
    for (let tx = x0; tx <= x1; tx++) {
      const col = tx - x0, row = ty - y0;
      const tileURL = `https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/${zoom}/${ty}/${tx}`;
      const proxied = `/api/tile-proxy?url=${encodeURIComponent(tileURL)}`;
      fetches.push(
        fetch(proxied).then(r => r.blob())
          .then(blob => createImageBitmap(blob))
          .then(bmp => ctx.drawImage(bmp, col * TILE_SIZE, row * TILE_SIZE))
          .catch(() => {}) // skip failed tiles
      );
    }
  }
  await Promise.all(fetches);

  const texture = new THREE.CanvasTexture(canvas);
  groundPlane.material.map = texture;
  groundPlane.material.needsUpdate = true;

  // Size the ground plane in ENU metres
  const latM  = latSpan * 111320;
  const lonM  = lonSpan * 111320 * Math.cos(((bounds.north + bounds.south) / 2) * Math.PI / 180);
  groundPlane.scale.set(lonM, latM, 1);
  groundPlane.position.set(0, state.gridMeta ? state.gridMeta.min_up : -1, 0);
  groundPlane.visible = state.showGround;
}

function latLonToTile(lat, lon, zoom) {
  const n = Math.pow(2, zoom);
  const x = Math.floor((lon + 180) / 360 * n);
  const latR = lat * Math.PI / 180;
  const y = Math.floor((1 - Math.log(Math.tan(latR) + 1 / Math.cos(latR)) / Math.PI) / 2 * n);
  return { x: Math.max(0, x), y: Math.max(0, y) };
}

function chooseTileZoom(latSpan, lonSpan) {
  // Aim for a 2–4 tile grid (256px each); pick zoom so max span fits in ~4 tiles.
  const maxSpan = Math.max(latSpan, lonSpan);
  for (let z = 18; z >= 1; z--) {
    const tileSpan = 360 / Math.pow(2, z);
    if (maxSpan > tileSpan) return z + 1;
  }
  return 15;
}

// ── 3D view toggle buttons ────────────────────────────────────────────────────
function makeToggle(btnId, stateKey, mesh) {
  document.getElementById(btnId).addEventListener('click', function() {
    state[stateKey] = !state[stateKey];
    this.classList.toggle('on', state[stateKey]);
    this.textContent = this.textContent.replace(/✓|✗/, state[stateKey] ? '✓' : '✗');
    if (mesh) mesh.visible = state[stateKey];
  });
}
makeToggle('btn-toggle-voxels', 'showVoxels', null);
makeToggle('btn-toggle-cams',   'showCams',   camGroup);
makeToggle('btn-toggle-rays',   'showRays',   null);
makeToggle('btn-toggle-ground', 'showGround', groundPlane);

// ── Renderer resize ───────────────────────────────────────────────────────────
function resizeRenderer() {
  const w = wrap3d.clientWidth, h = wrap3d.clientHeight;
  renderer.setSize(w, h);
  cam3d.aspect = w / (h || 1);
  cam3d.updateProjectionMatrix();
}
window.addEventListener('resize', resizeRenderer);
resizeRenderer();

function animate() {
  requestAnimationFrame(animate);
  renderer.render(scene, cam3d);
}
animate();

// ── Sidebar tabs ──────────────────────────────────────────────────────────────
document.querySelectorAll('.wv-tab').forEach(tab => {
  tab.addEventListener('click', () => {
    document.querySelectorAll('.wv-tab').forEach(t => t.classList.remove('active'));
    document.querySelectorAll('.wv-sidebar-pane').forEach(p => p.classList.remove('active'));
    tab.classList.add('active');
    document.getElementById('pane-' + tab.dataset.pane).classList.add('active');
  });
});

// ── Target list ───────────────────────────────────────────────────────────────
function updateTargetList(targets) {
  const el = document.getElementById('target-list');
  if (!targets.length) {
    el.innerHTML = '<div class="none-msg">None detected.</div>';
    return;
  }
  el.innerHTML = targets.map((t, i) => `
    <div class="target-card">
      <div class="target-label">TARGET ${i + 1}</div>
      <div class="target-pos">
        E ${t.position[0].toFixed(1)}&nbsp;
        N ${t.position[1].toFixed(1)}&nbsp;
        U ${t.position[2].toFixed(1)}&nbsp;m
      </div>
      <div class="target-conf">
        conf ${(t.confidence * 100).toFixed(0)}%
        ${t.latlon ? ` · ${t.latlon[0].toFixed(5)}, ${t.latlon[1].toFixed(5)}` : ''}
      </div>
    </div>`).join('');

  // Switch sidebar to targets tab automatically when first detection
  if (targets.length > 0 &&
      document.querySelector('.wv-tab.active').dataset.pane !== 'targets') {
    // only auto-switch once
  }
}

// ── Config panel ──────────────────────────────────────────────────────────────
function wireSlider(id, valId, dec) {
  const sl  = document.getElementById(id);
  const val = document.getElementById(valId);
  sl.addEventListener('input', () => { val.textContent = parseFloat(sl.value).toFixed(dec); });
}
wireSlider('cfg-decay',      'val-decay',      3);
wireSlider('cfg-threshold',  'val-threshold',  2);
wireSlider('cfg-motion',     'val-motion',     0);
wireSlider('cfg-vox-thresh', 'val-vox-thresh', 2);

document.getElementById('cfg-vox-thresh').addEventListener('input', e => {
  state.voxThreshold = parseFloat(e.target.value);
});

document.getElementById('btn-apply-cfg').addEventListener('click', () => {
  fetch('/api/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      decay_factor:        parseFloat(document.getElementById('cfg-decay').value),
      detection_threshold: parseFloat(document.getElementById('cfg-threshold').value),
      motion_threshold:    parseInt(document.getElementById('cfg-motion').value, 10),
    }),
  }).then(r => r.ok ? toast('Config applied.') : toast('Config failed.'))
    .catch(e => toast('Error: ' + e.message));
});

function doResetGrid() {
  fetch('/api/reset-grid', { method: 'POST' })
    .then(() => toast('Grid reset.'))
    .catch(e => toast('Error: ' + e.message));
}
document.getElementById('btn-reset-grid').addEventListener('click', doResetGrid);
document.getElementById('btn-reset-grid2').addEventListener('click', doResetGrid);

// ── Toolbar buttons ───────────────────────────────────────────────────────────
document.getElementById('btn-add-camera').addEventListener('click', () => {
  // No specific location yet — open config modal directly, placement optional
  openCameraModal(null);
});

document.getElementById('btn-place-camera').addEventListener('click', function() {
  if (place.mode !== 'idle') {
    setPlaceMode('idle');
  } else {
    setPlaceMode('origin');
  }
});

// ── API status check ──────────────────────────────────────────────────────────
async function checkOriginStatus() {
  try {
    const r = await fetch('/api/status');
    const data = await r.json();
    if (data.origin_set) {
      setBadge('origin', 'on', 'origin set');
    }
  } catch(e) {}
}

// ── Utility ───────────────────────────────────────────────────────────────────
function esc(s) {
  return String(s)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ── Phone client visualisation ────────────────────────────────────────────────
// Called every WS tick. Phones are identified by type === "phone" (or absent type
// for older clients) and are distinct from webcam entries configured locally.

function phoneIcon(heading) {
  // Rotate the 📱 arrow to show compass heading visually.
  return L.divIcon({
    className: '',
    html: `<div style="
      width:26px;height:26px;border-radius:50%;
      background:rgba(59,158,255,0.85);border:2px solid #fff;
      display:flex;align-items:center;justify-content:center;
      font-size:13px;transform:rotate(${heading}deg);
      box-shadow:0 2px 8px rgba(0,0,0,0.6)">📱</div>`,
    iconSize: [26, 26],
    iconAnchor: [13, 13],
  });
}

function updatePhoneClients(clients) {
  const phones = clients.filter(c => c.type !== 'webcam');
  const seenIDs = new Set(phones.map(c => c.id));

  // Remove disconnected phones
  phoneMarkers.forEach((marker, id) => {
    if (!seenIDs.has(id)) {
      map.removeLayer(marker);
      phoneMarkers.delete(id);
      phoneClientMap.delete(id);
      removePhoneFeedTile(id);
    }
  });

  // Add / update phones
  phones.forEach(c => {
    phoneClientMap.set(c.id, c);

    if (c.lat === 0 && c.lon === 0) return; // no GPS yet

    if (phoneMarkers.has(c.id)) {
      // Update position and icon
      phoneMarkers.get(c.id).setLatLng([c.lat, c.lon]);
      phoneMarkers.get(c.id).setIcon(phoneIcon(c.heading));
    } else {
      // New phone — add marker
      const marker = L.marker([c.lat, c.lon], {
        icon: phoneIcon(c.heading),
        title: c.id,
      }).addTo(map);
      marker.bindTooltip(`${c.id}<br>${c.fps.toFixed(1)} fps ±${c.gps_accuracy.toFixed(0)}m`, {
        permanent: false, direction: 'top',
      });
      phoneMarkers.set(c.id, marker);
    }

    // Add feed tile if not already present
    if (!phoneFeedIDs.has(c.id)) {
      addPhoneFeedTile(c);
    }
  });

  // Refresh phone section in cameras sidebar
  renderPhoneSidebarSection(phones);
}

function addPhoneFeedTile(c) {
  phoneFeedIDs.add(c.id);
  const feeds = document.getElementById('wv-feeds');
  const group = document.createElement('div');
  group.className = 'feed-group';
  group.id = 'phone-feeds-' + c.id;
  group.innerHTML = `
    <div class="feed-group-label" style="color:var(--accent)">📱 ${esc(c.id)}</div>
    <div style="display:flex;gap:2px">
      <div class="feed-tile raw" style="height:72px;border-top-color:var(--accent)">
        <img src="/client/feed/${c.id}" alt="phone feed">
        <div class="feed-tile-label">Live</div>
      </div>
    </div>`;
  feeds.appendChild(group);
}

function removePhoneFeedTile(id) {
  phoneFeedIDs.delete(id);
  const el = document.getElementById('phone-feeds-' + id);
  if (el) el.remove();
}

// Phone entries rendered at the bottom of the Cameras sidebar pane.
function renderPhoneSidebarSection(phones) {
  let el = document.getElementById('phone-client-section');
  if (!phones.length) {
    if (el) el.remove();
    return;
  }

  if (!el) {
    el = document.createElement('div');
    el.id = 'phone-client-section';
    el.className = 'wv-section';
    document.getElementById('cam-list').parentElement.appendChild(el);
  }

  const age = id => {
    const c = phoneClientMap.get(id);
    if (!c) return 999;
    return Date.now() / 1000 - c.last_seen;
  };

  el.innerHTML = `
    <div class="wv-section-title" style="color:var(--accent)">Phone Clients</div>
    ${phones.map(c => {
      const a = age(c.id);
      const dot = a < 2 ? 'on' : a < 5 ? 'warn' : 'err';
      return `
        <div class="cam-card">
          <div class="cam-card-header">
            <div class="cam-dot ${dot === 'on' ? 'live' : dot === 'warn' ? '' : 'err'}"></div>
            <span class="cam-name">📱 ${esc(c.id)}</span>
            <span class="cam-fps">${c.fps.toFixed(1)}fps</span>
          </div>
          <div class="cam-meta">
            <span>${c.lat.toFixed(5)}, ${c.lon.toFixed(5)}</span>
            <span>hdg ${c.heading.toFixed(0)}°</span>
            <span>±${c.gps_accuracy.toFixed(0)}m</span>
          </div>
        </div>`;
    }).join('')}`;
}

// ── Boot ──────────────────────────────────────────────────────────────────────
(async () => {
  await loadCameras();
  initFeeds();
  await checkOriginStatus();

  // If we have cameras with known lat/lon, centre the map on them
  if (state.cameras.length > 0) {
    const lats = state.cameras.map(c => c.lat);
    const lons = state.cameras.map(c => c.lon);
    const clat = lats.reduce((a, b) => a + b, 0) / lats.length;
    const clon = lons.reduce((a, b) => a + b, 0) / lons.length;
    if (clat !== 0 || clon !== 0) map.setView([clat, clon], 16);
  }
})();
