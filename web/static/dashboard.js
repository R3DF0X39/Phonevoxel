'use strict';

// ── WebSocket ─────────────────────────────────────────────────────────────────
const WS_URL = (location.protocol === 'https:' ? 'wss://' : 'ws://') +
    location.host + '/ws/dashboard';

let ws        = null;
let gridMeta  = null;   // { min_east, max_east, min_north, max_north, ... nx, ny, nz }
let lastState = null;   // most recent DashboardUpdate

function openWS() {
    ws = new WebSocket(WS_URL);
    ws.onopen  = () => console.log('Dashboard WS connected');
    ws.onclose = () => { setTimeout(openWS, 3000); };
    ws.onerror = () => ws.close();
    ws.onmessage = e => {
        try {
            const data = JSON.parse(e.data);
            lastState = data;
            gridMeta  = data.grid_meta;
            updateClientList(data.clients || []);
            updateTargetList(data.targets || []);
            updateHeatmap(data.sparse_voxels || [], data.grid_meta);
            update3D(data.sparse_voxels || [], data.targets || [], data.clients || [], data.grid_meta);
            document.getElementById('client-count').textContent =
                (data.clients || []).length + ' client' +
                ((data.clients || []).length !== 1 ? 's' : '');
        } catch(err) {
            console.error('WS parse error', err);
        }
    };
}

openWS();

// ── Client list ───────────────────────────────────────────────────────────────
function updateClientList(clients) {
    const el = document.getElementById('client-list');
    if (!clients.length) {
        el.innerHTML = '<div style="color:#555;font-size:11px">None</div>';
        return;
    }
    el.innerHTML = clients.map(c => `
        <div class="client-row">
          <div class="dot ${dotColor(c.last_seen)}"></div>
          <span class="client-id">${c.id}</span>
          <span class="client-fps">${c.fps.toFixed(1)}fps</span>
        </div>
        <div class="client-gps" style="padding-left:18px;margin-bottom:4px">
          ±${c.gps_accuracy.toFixed(0)}m &nbsp; hdg ${c.heading.toFixed(0)}°
        </div>
    `).join('');
}

function dotColor(lastSeen) {
    const age = Date.now() / 1000 - lastSeen;
    if (age < 2)  return 'green';
    if (age < 5)  return 'yellow';
    return 'red';
}

// ── Target list ───────────────────────────────────────────────────────────────
function updateTargetList(targets) {
    const el = document.getElementById('target-list');
    if (!targets.length) {
        el.innerHTML = '<div style="color:#555;font-size:11px">None</div>';
        return;
    }
    el.innerHTML = targets.map((t, i) => `
        <div class="target-row">
          <div class="target-pos">T${i+1}: E${t.position[0].toFixed(1)} N${t.position[1].toFixed(1)} U${t.position[2].toFixed(1)}m</div>
          <div class="target-conf">conf ${(t.confidence * 100).toFixed(0)}% &nbsp;
            ${t.latlon ? t.latlon[0].toFixed(5) + ', ' + t.latlon[1].toFixed(5) : ''}</div>
        </div>
    `).join('');
}

// ── Config panel ──────────────────────────────────────────────────────────────
function wireSlider(sliderId, valId, decimals) {
    const sl  = document.getElementById(sliderId);
    const val = document.getElementById(valId);
    sl.addEventListener('input', () => {
        val.textContent = parseFloat(sl.value).toFixed(decimals);
    });
}
wireSlider('cfg-decay',     'val-decay',     3);
wireSlider('cfg-threshold', 'val-threshold', 2);
wireSlider('cfg-motion',    'val-motion',    0);

document.getElementById('btn-apply-cfg').addEventListener('click', () => {
    const body = JSON.stringify({
        decay_factor:        parseFloat(document.getElementById('cfg-decay').value),
        detection_threshold: parseFloat(document.getElementById('cfg-threshold').value),
        motion_threshold:    parseInt(  document.getElementById('cfg-motion').value, 10),
    });
    fetch('/api/config', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body })
        .then(r => r.ok ? console.log('Config updated') : console.warn('Config update failed', r.status))
        .catch(console.error);
});

document.getElementById('btn-reset-grid').addEventListener('click', () => {
    fetch('/api/reset-grid', { method: 'POST' })
        .then(() => console.log('Grid reset'))
        .catch(console.error);
});

// ── 2D Heatmap ────────────────────────────────────────────────────────────────
const heatCanvas = document.getElementById('heatmap-canvas');
const heatCtx    = heatCanvas.getContext('2d');

function resizeHeatmap() {
    const parent = heatCanvas.parentElement;
    heatCanvas.width  = parent.clientWidth;
    heatCanvas.height = parent.clientHeight;
}
window.addEventListener('resize', resizeHeatmap);
resizeHeatmap();

// Build a top-down (East-North) projection: max value along vertical axis
function buildTopDown(voxels, meta) {
    if (!meta) return null;
    const { nx, ny } = meta;
    const grid = new Float32Array(nx * ny);
    for (const v of voxels) {
        const idx = v.iy * nx + v.ix;
        if (v.v > grid[idx]) grid[idx] = v.v;
    }
    return grid;
}

function valueToColor(v) {
    // Blue → Cyan → Green → Yellow → Red
    const t  = Math.min(v, 1);
    const r  = Math.min(1, t * 2)        * 255 | 0;
    const g  = Math.min(1, t < 0.5 ? t * 2 : (1 - t) * 2) * 255 | 0;
    const b  = Math.max(0, 1 - t * 2)   * 255 | 0;
    return `rgb(${r},${g},${b})`;
}

function updateHeatmap(voxels, meta) {
    if (!meta) return;
    const { width: cw, height: ch } = heatCanvas;
    heatCtx.clearRect(0, 0, cw, ch);

    const grid = buildTopDown(voxels, meta);
    if (!grid) return;

    const { nx, ny } = meta;
    const cellW = cw / nx;
    const cellH = ch / ny;

    // Draw background
    heatCtx.fillStyle = '#060610';
    heatCtx.fillRect(0, 0, cw, ch);

    // Draw voxels
    for (let iy = 0; iy < ny; iy++) {
        for (let ix = 0; ix < nx; ix++) {
            const v = grid[iy * nx + ix];
            if (v < 0.02) continue;
            heatCtx.fillStyle = valueToColor(v);
            // Note: iy=0 is south (min_north), draw from bottom
            const px = ix * cellW;
            const py = (ny - 1 - iy) * cellH;
            heatCtx.fillRect(px, py, Math.max(1, cellW), Math.max(1, cellH));
        }
    }

    // Draw clients
    if (lastState && lastState.clients) {
        for (const c of lastState.clients) {
            if (!meta) continue;
            const e = c.east || 0, n = c.north || 0;
            const px = ((e - meta.min_east)  / (meta.max_east  - meta.min_east)) * cw;
            const py = (1 - (n - meta.min_north) / (meta.max_north - meta.min_north)) * ch;
            drawArrow(heatCtx, px, py, c.heading || 0, '#4af', 10);
        }
    }

    // Axis labels
    heatCtx.fillStyle = '#334';
    heatCtx.font = '9px monospace';
    heatCtx.fillText(`E ${meta.min_east.toFixed(0)}`, 2, ch - 2);
    heatCtx.fillText(`E ${meta.max_east.toFixed(0)}`, cw - 32, ch - 2);
    heatCtx.fillText(`N ${meta.max_north.toFixed(0)}`, 2, 10);
}

function drawArrow(ctx, x, y, headingDeg, color, size) {
    const rad = (headingDeg - 90) * Math.PI / 180; // 0°=north → up
    ctx.save();
    ctx.translate(x, y);
    ctx.rotate(rad);
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(0, -size);
    ctx.lineTo(0, size * 0.5);
    ctx.moveTo(0, -size);
    ctx.lineTo(-size * 0.4, -size * 0.4);
    ctx.moveTo(0, -size);
    ctx.lineTo(size * 0.4, -size * 0.4);
    ctx.stroke();
    ctx.restore();
}

// Click on heatmap → ray query
heatCanvas.addEventListener('click', e => {
    if (!gridMeta || !lastState || !lastState.clients || !lastState.clients.length) return;
    const rect = heatCanvas.getBoundingClientRect();
    const fx   = (e.clientX - rect.left)  / rect.width;
    const fy   = (e.clientY - rect.top)   / rect.height;
    const east  = gridMeta.min_east  + fx * (gridMeta.max_east  - gridMeta.min_east);
    const north = gridMeta.min_north + (1 - fy) * (gridMeta.max_north - gridMeta.min_north);
    queryByEastNorth(east, north);
});

function queryByEastNorth(east, north) {
    if (!lastState || !lastState.clients || !lastState.clients.length) return;
    // Use the first client as the ray origin reference
    const clientId = lastState.clients[0].id;
    // We project a vertical ray at the clicked column
    // (This is a simplified query; a proper tap-to-locate would use the client's camera ray)
    fetch('/api/query-ray', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            client_id: clientId,
            pixel_x: east,   // repurposed: server interprets as column position
            pixel_y: north,
        }),
    })
    .then(r => r.json())
    .then(resp => {
        const el = document.getElementById('query-result');
        if (resp.found) {
            el.innerHTML = `<span style="color:#fa4">
                E${resp.position[0].toFixed(1)} N${resp.position[1].toFixed(1)} U${resp.position[2].toFixed(1)}m<br>
                val: ${resp.value.toFixed(3)}<br>
                ${resp.latlon[0].toFixed(5)}, ${resp.latlon[1].toFixed(5)}
            </span>`;
        } else {
            el.textContent = 'No voxel found';
        }
    })
    .catch(err => console.error('Query error', err));
}

// ── Three.js 3D view ──────────────────────────────────────────────────────────
const view3d = document.getElementById('view-3d');

const renderer = new THREE.WebGLRenderer({ antialias: true });
renderer.setPixelRatio(window.devicePixelRatio);
renderer.setClearColor(0x060610);
view3d.appendChild(renderer.domElement);

const scene  = new THREE.Scene();
const camera = new THREE.PerspectiveCamera(50, 1, 0.1, 5000);
camera.position.set(0, -150, 80);
camera.lookAt(0, 0, 0);

// Orbit controls (manual implementation — no import needed)
let isDragging = false, lastMX = 0, lastMY = 0;
let theta = 0, phi = Math.PI / 4, radius = 200;

function updateCameraOrbit() {
    camera.position.x = radius * Math.sin(phi) * Math.sin(theta);
    camera.position.y = -radius * Math.sin(phi) * Math.cos(theta);
    camera.position.z = radius * Math.cos(phi);
    camera.lookAt(0, 0, 0);
}
updateCameraOrbit();

renderer.domElement.addEventListener('mousedown', e => { isDragging = true; lastMX = e.clientX; lastMY = e.clientY; });
window.addEventListener('mouseup', () => { isDragging = false; });
window.addEventListener('mousemove', e => {
    if (!isDragging) return;
    const dx = e.clientX - lastMX, dy = e.clientY - lastMY;
    lastMX = e.clientX; lastMY = e.clientY;
    theta += dx * 0.005;
    phi = Math.max(0.1, Math.min(Math.PI - 0.1, phi + dy * 0.005));
    updateCameraOrbit();
});
renderer.domElement.addEventListener('wheel', e => {
    radius = Math.max(10, Math.min(2000, radius + e.deltaY * 0.5));
    updateCameraOrbit();
}, { passive: true });

// Add grid axes helper
const axesHelper = new THREE.AxesHelper(20);
scene.add(axesHelper); // X=east(red), Y=north(green), Z=up(blue) in Three.js

// Instanced mesh for voxels
const MAX_VOXELS = 50000;
const boxGeo     = new THREE.BoxGeometry(1, 1, 1);
const boxMat     = new THREE.MeshBasicMaterial({ vertexColors: true });
let   voxelMesh  = new THREE.InstancedMesh(boxGeo, boxMat, MAX_VOXELS);
voxelMesh.count  = 0;
scene.add(voxelMesh);

// Target spheres pool
const targetPool = [];
const sphereGeo  = new THREE.SphereGeometry(1.5, 8, 8);
const sphereMat  = new THREE.MeshBasicMaterial({ color: 0xff4444, wireframe: true });
for (let i = 0; i < 32; i++) {
    const m = new THREE.Mesh(sphereGeo, sphereMat);
    m.visible = false;
    scene.add(m);
    targetPool.push(m);
}

const dummy    = new THREE.Object3D();
const colorObj = new THREE.Color();

function valueToThreeColor(v) {
    const t = Math.min(v, 1);
    colorObj.setHSL((1 - t) * 0.66, 1, 0.5); // blue→red hue sweep
    return colorObj.clone();
}

function update3D(voxels, targets, clients, meta) {
    if (!meta) return;

    // Voxels — Three.js: X=east, Y=up, Z=-north (right-hand)
    const count = Math.min(voxels.length, MAX_VOXELS);
    const res   = meta.resolution;

    for (let i = 0; i < count; i++) {
        const v  = voxels[i];
        const e  = meta.min_east  + (v.ix + 0.5) * res;
        const n  = meta.min_north + (v.iy + 0.5) * res;
        const u  = meta.min_up    + (v.iz + 0.5) * res;

        dummy.position.set(e, u, -n);
        dummy.scale.setScalar(res * 0.9);
        dummy.updateMatrix();
        voxelMesh.setMatrixAt(i, dummy.matrix);
        voxelMesh.setColorAt(i, valueToThreeColor(v.v));
    }
    voxelMesh.count = count;
    voxelMesh.instanceMatrix.needsUpdate = true;
    if (voxelMesh.instanceColor) voxelMesh.instanceColor.needsUpdate = true;

    // Targets
    targetPool.forEach((m, i) => {
        if (i < targets.length) {
            const t = targets[i];
            m.position.set(t.position[0], t.position[2], -t.position[1]);
            m.visible = true;
        } else {
            m.visible = false;
        }
    });
}

function resizeRenderer() {
    const w = view3d.clientWidth, h = view3d.clientHeight;
    renderer.setSize(w, h);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
}
window.addEventListener('resize', resizeRenderer);
resizeRenderer();

function animate() {
    requestAnimationFrame(animate);
    renderer.render(scene, camera);
}
animate();
