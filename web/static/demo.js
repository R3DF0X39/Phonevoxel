'use strict';

// ── Wire protocol constants (must match internal/server/parse.go) ─────────────
const HEADER_SIZE = 84;
const FRAME_W     = 320;
const FRAME_H     = 240;

// ── Flat-earth GPS conversion (matches internal/geo/coordinate.go) ────────────
// ENU origin placed at GPS (0°, 0°) so the server's auto-origin will be set
// from the first camera frame, and all subsequent cameras are consistently offset.
const METERS_PER_DEG = 111320.0;

function enuToGPS(e, n, u) {
    // lat=0 origin → cos(lat)=1, so both axes use the same scale
    return { lat: n / METERS_PER_DEG, lon: e / METERS_PER_DEG, alt: u };
}

// ── ZXY Euler → rotation matrix (mirrors geo/orientation.go) ─────────────────
// R satisfies: v_camera = R * v_world
function buildR(alphaDeg, betaDeg, gammaDeg) {
    const a = alphaDeg * Math.PI / 180;
    const b = betaDeg  * Math.PI / 180;
    const g = gammaDeg * Math.PI / 180;
    const cA = Math.cos(a), sA = Math.sin(a);
    const cB = Math.cos(b), sB = Math.sin(b);
    const cG = Math.cos(g), sG = Math.sin(g);
    return [
        [cA*cG - sA*sB*sG, -cB*sA, cA*sG + cG*sA*sB],
        [cG*sA + cA*sB*sG,  cA*cB, sA*sG - cA*cG*sB],
        [-cB*sG,             sB,    cB*cG            ],
    ];
}

// ── W3C angles for a desired camera direction ─────────────────────────────────
// Derives W3C (alpha, beta, gamma) such that the camera (Z-forward) looks toward
// azimuth az_deg (compass, CW from North) and elevation el_deg (above horizontal).
//
// Derivation:  camera forward in ENU = R^T * [0,0,1] = R[2] (third row).
// R[2] = [-cosB*sinG, sinB, cosB*cosG]
// Setting this equal to [sin(az)*cos(el), cos(az)*cos(el), sin(el)] and solving:
//   sinB = cos(az)*cos(el)
//   sinG = -sin(az)*cos(el) / cosB
//   cosG =  sin(el) / cosB
//   alpha is free (only rotates camera's X/Y axes); we use 0.
//
// The blob is placed at the centre pixel because each camera aims directly at
// the target, so the target vector is exactly along the forward axis.
function anglesForDirection(azDeg, elDeg, depth) {
    depth = depth || 0;
    if (depth > 10) {
        // Should never happen; return a safe fallback
        return { alpha: 0, beta: 45, gamma: 0 };
    }

    const az = azDeg * Math.PI / 180;
    const el = elDeg * Math.PI / 180;

    const sinB = Math.cos(az) * Math.cos(el);
    const cosB = Math.sqrt(Math.max(0, 1 - sinB * sinB));

    if (cosB < 1e-4) {
        // Gimbal lock (camera aimed exactly N/S horizontally); perturb elevation.
        const nudge = elDeg >= 0 ? 1 : -1;
        return anglesForDirection(azDeg, elDeg + nudge, depth + 1);
    }

    const sinG = -Math.sin(az) * Math.cos(el) / cosB;
    const cosG =  Math.sin(el)                / cosB;

    return {
        alpha: 0,
        beta:  Math.asin(Math.max(-1, Math.min(1, sinB))) * 180 / Math.PI,
        gamma: Math.atan2(sinG, cosG) * 180 / Math.PI,
    };
}

// ── Binary message builder ────────────────────────────────────────────────────
const tsOffset = Date.now() / 1000 - performance.now() / 1000;

function buildMsg(gps, alpha, beta, gamma, hfov, jpegBuf) {
    const buf = new ArrayBuffer(HEADER_SIZE + jpegBuf.byteLength);
    const v   = new DataView(buf);
    const f64 = (off, val) => v.setFloat64(off, val, true);
    const u32 = (off, val) => v.setUint32(off, val, true);

    f64(0,  performance.now() / 1000 + tsOffset);
    f64(8,  gps.lat);
    f64(16, gps.lon);
    f64(24, gps.alt);
    f64(32, 1.0);   // GPS accuracy: 1 m (synthetic)
    f64(40, alpha);
    f64(48, beta);
    f64(56, gamma);
    f64(64, hfov);
    u32(72, FRAME_W);
    u32(76, FRAME_H);
    u32(80, jpegBuf.byteLength);
    new Uint8Array(buf, HEADER_SIZE).set(new Uint8Array(jpegBuf));
    return buf;
}

// ── Synthetic frame generator ─────────────────────────────────────────────────
// Single shared offscreen canvas for all cameras (generation is serialised by
// the async send loop, so no concurrency issue).
const synthCanvas = document.createElement('canvas');
synthCanvas.width  = FRAME_W;
synthCanvas.height = FRAME_H;
const synthCtx = synthCanvas.getContext('2d');

// Returns Promise<ArrayBuffer> — a JPEG of a blank or blob frame.
// The blob is drawn at the image centre because each camera aims directly at
// the target (see anglesForDirection), so the target projects to (cx, cy).
function makeFrame(showBlob) {
    synthCtx.fillStyle = 'rgb(30,30,30)';
    synthCtx.fillRect(0, 0, FRAME_W, FRAME_H);

    if (showBlob) {
        const cx = FRAME_W / 2, cy = FRAME_H / 2;
        const grad = synthCtx.createRadialGradient(cx, cy, 0, cx, cy, 24);
        grad.addColorStop(0, 'rgba(210,210,210,1)');
        grad.addColorStop(1, 'rgba(210,210,210,0)');
        synthCtx.fillStyle = grad;
        synthCtx.fillRect(0, 0, FRAME_W, FRAME_H);
    }

    return new Promise(resolve =>
        synthCanvas.toBlob(blob => blob.arrayBuffer().then(resolve), 'image/jpeg', 0.8)
    );
}

// ── VirtualCamera ─────────────────────────────────────────────────────────────
class VirtualCamera {
    constructor(id, camE, camN, camU, targetE, targetN, targetU, hfov, rateMs) {
        this.id = id;

        // GPS position of this camera
        this.gps = enuToGPS(camE, camN, camU);

        // Direction from camera toward target
        const dE    = targetE - camE;
        const dN    = targetN - camN;
        const dU    = targetU - camU;
        const horiz = Math.sqrt(dE * dE + dN * dN);
        const az    = Math.atan2(dE, dN) * 180 / Math.PI; // compass bearing
        const el    = Math.atan2(dU, horiz) * 180 / Math.PI;

        const ang   = anglesForDirection(az, el);
        this.alpha  = ang.alpha;
        this.beta   = ang.beta;
        this.gamma  = ang.gamma;
        this.hfov   = hfov;
        this.rateMs = rateMs;

        this.ws         = null;
        this.timer      = null;
        this.toggle     = false;
        this.framesSent = 0;
        this.onStatus   = null; // ({id, state, framesSent}) callback
    }

    start() {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        this.ws = new WebSocket(`${proto}//${location.host}/ws/client`);
        this.ws.binaryType = 'arraybuffer';

        this.ws.onopen = () => {
            this._emit('connected');
            this.timer = setInterval(() => this._tick(), this.rateMs);
        };

        this.ws.onclose = () => {
            if (this.timer) { clearInterval(this.timer); this.timer = null; }
            this._emit('disconnected');
        };

        this.ws.onerror = () => this.ws.close();
    }

    stop() {
        if (this.timer) { clearInterval(this.timer); this.timer = null; }
        if (this.ws)    { this.ws.close(); this.ws = null; }
    }

    async _tick() {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        this.toggle = !this.toggle;
        const jpeg  = await makeFrame(this.toggle);
        const msg   = buildMsg(this.gps, this.alpha, this.beta, this.gamma, this.hfov, jpeg);
        try {
            this.ws.send(msg);
            this.framesSent++;
        } catch (_) {}
        this._emit('streaming');
    }

    _emit(state) {
        if (this.onStatus) this.onStatus({ id: this.id, state, framesSent: this.framesSent });
    }
}

// ── Demo controller ───────────────────────────────────────────────────────────
let activeCameras = [];

function buildCameraRing(cfg) {
    const { count, radius, camHeight, hfov, rateMs, targetE, targetN, targetU } = cfg;
    const cameras = [];
    for (let i = 0; i < count; i++) {
        const az  = (360 / count) * i; // azimuth from target to this camera
        const cE  = targetE + radius * Math.sin(az * Math.PI / 180);
        const cN  = targetN + radius * Math.cos(az * Math.PI / 180);
        const cU  = camHeight;
        cameras.push(new VirtualCamera(
            i + 1, cE, cN, cU, targetE, targetN, targetU, hfov, rateMs
        ));
    }
    return cameras;
}

async function startDemo() {
    const cfg = readCfg();

    // Reset geo origin so these virtual cameras establish a fresh ENU frame
    await fetch('/api/reset-origin', { method: 'POST' });
    // Reset voxel grid so previous data doesn't confuse the display
    await fetch('/api/reset-grid',   { method: 'POST' });

    stopDemo(/* silent */ true);

    activeCameras = buildCameraRing(cfg);
    buildStatusRows(activeCameras.length);

    activeCameras.forEach(cam => {
        cam.onStatus = ({ id, state, framesSent }) => {
            const row = document.getElementById('row-' + id);
            if (!row) return;
            row.cells[1].textContent = state;
            row.cells[2].textContent = framesSent + ' sent';
        };
        cam.start();
    });

    document.getElementById('btn-start').disabled = true;
    document.getElementById('btn-stop').disabled  = false;
}

function stopDemo(silent) {
    activeCameras.forEach(c => c.stop());
    activeCameras = [];
    if (!silent) {
        document.getElementById('btn-start').disabled = false;
        document.getElementById('btn-stop').disabled  = true;
    }
}

// ── Config reader ─────────────────────────────────────────────────────────────
function readCfg() {
    return {
        count:     parseInt( document.getElementById('cam-count').value,  10),
        radius:    parseFloat(document.getElementById('cam-radius').value),
        camHeight: parseFloat(document.getElementById('cam-height').value),
        hfov:      parseFloat(document.getElementById('cam-hfov').value),
        rateMs:    Math.round(1000 / parseFloat(document.getElementById('frame-rate').value)),
        targetE:   parseFloat(document.getElementById('target-e').value),
        targetN:   parseFloat(document.getElementById('target-n').value),
        targetU:   parseFloat(document.getElementById('target-u').value),
    };
}

function buildStatusRows(count) {
    const tbody = document.getElementById('status-body');
    tbody.innerHTML = '';
    for (let i = 1; i <= count; i++) {
        const tr = document.createElement('tr');
        tr.id = 'row-' + i;
        tr.innerHTML = `<td>Cam ${i}</td><td>—</td><td>0 sent</td>`;
        tbody.appendChild(tr);
    }
}

// ── Slider live labels ────────────────────────────────────────────────────────
document.querySelectorAll('input[type=range]').forEach(el => {
    const valEl = document.getElementById(el.id + '-val');
    if (valEl) {
        valEl.textContent = el.value;
        el.addEventListener('input', () => { valEl.textContent = el.value; });
    }
});

// ── Button wiring ─────────────────────────────────────────────────────────────
document.getElementById('btn-start').addEventListener('click', startDemo);
document.getElementById('btn-stop').addEventListener('click', () => stopDemo(false));
