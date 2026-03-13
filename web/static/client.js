'use strict';

// ── Configuration from URL params ────────────────────────────────────────────
const params = new URLSearchParams(location.search);
const SEND_RATE_HZ  = parseFloat(params.get('rate')       || '4');
const JPEG_QUALITY  = parseFloat(params.get('quality')    || '0.7');
const CAP_WIDTH     = parseInt(  params.get('resolution') || '640', 10);
const SERVER_URL    = params.get('server') ||
    (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws/client';
const SEND_RATE_MS  = Math.round(1000 / SEND_RATE_HZ);

// ── Phone FOV database ────────────────────────────────────────────────────────
const PHONE_FOV_DATABASE = [
    { label: '-- Select your phone --',               hfov: 70.0 },
    // Apple
    { label: 'iPhone 16 Pro / Pro Max (Main)',         hfov: 69.5 },
    { label: 'iPhone 16 / Plus (Main)',                hfov: 69.5 },
    { label: 'iPhone 15 Pro / Pro Max (Main)',         hfov: 69.5 },
    { label: 'iPhone 15 / Plus (Main)',                hfov: 69.5 },
    { label: 'iPhone 14 Pro / Pro Max (Main)',         hfov: 69.5 },
    { label: 'iPhone 14 / Plus (Main)',                hfov: 69.5 },
    { label: 'iPhone 13 Pro / Pro Max (Main)',         hfov: 69.5 },
    { label: 'iPhone 13 / Mini (Main)',                hfov: 69.5 },
    { label: 'iPhone 12 Pro / Pro Max (Main)',         hfov: 69.5 },
    { label: 'iPhone 12 / Mini (Main)',                hfov: 69.5 },
    { label: 'iPhone 11 Pro / Pro Max (Main)',         hfov: 65.0 },
    { label: 'iPhone 11 (Main)',                       hfov: 65.0 },
    { label: 'iPhone SE (3rd gen)',                    hfov: 65.0 },
    { label: 'iPhone X / XS / XR (Main)',              hfov: 63.0 },
    { label: 'iPhone 13+ (Ultra Wide)',                hfov: 120.0 },
    // Samsung
    { label: 'Samsung Galaxy S24 Ultra (Main)',        hfov: 68.0 },
    { label: 'Samsung Galaxy S24 / S24+ (Main)',       hfov: 68.0 },
    { label: 'Samsung Galaxy S23 Ultra (Main)',        hfov: 68.0 },
    { label: 'Samsung Galaxy S23 / S23+ (Main)',       hfov: 68.0 },
    { label: 'Samsung Galaxy S22 Ultra (Main)',        hfov: 68.0 },
    { label: 'Samsung Galaxy S22 / S22+ (Main)',       hfov: 68.0 },
    { label: 'Samsung Galaxy S21 series (Main)',       hfov: 68.0 },
    { label: 'Samsung Galaxy A54 / A55 (Main)',        hfov: 68.0 },
    { label: 'Samsung Galaxy A34 / A35 (Main)',        hfov: 68.0 },
    // Google Pixel
    { label: 'Google Pixel 9 / Pro (Main)',            hfov: 74.0 },
    { label: 'Google Pixel 8 / Pro (Main)',            hfov: 74.0 },
    { label: 'Google Pixel 7 / Pro (Main)',            hfov: 74.0 },
    { label: 'Google Pixel 6 / Pro (Main)',            hfov: 74.0 },
    // OnePlus
    { label: 'OnePlus 12 (Main)',                      hfov: 72.0 },
    { label: 'OnePlus 11 (Main)',                      hfov: 72.0 },
    // Generic
    { label: 'Generic phone (narrow, ~60°)',           hfov: 60.0 },
    { label: 'Generic phone (standard, ~70°)',         hfov: 70.0 },
    { label: 'Generic phone (wide, ~80°)',             hfov: 80.0 },
    { label: 'Action cam / GoPro (~120°)',             hfov: 120.0 },
    { label: 'Custom (use slider)',                    hfov: null  },
];

// ── State ─────────────────────────────────────────────────────────────────────
let currentPosition    = null;
let currentOrientation = null;
let currentHFOV        = parseFloat(params.get('fov') || '70');
let ws                 = null;
let sendTimer          = null;
let gpsWatchId         = null;
let streaming          = false;

// Timing: compute a stable timestamp offset from performance.now()
const tsOffset = Date.now() / 1000 - performance.now() / 1000;

// ── DOM refs ──────────────────────────────────────────────────────────────────
const video          = document.getElementById('video-bg');
const capCanvas      = document.getElementById('cap-canvas');
const capCtx         = capCanvas.getContext('2d');
const connDot        = document.getElementById('conn-dot');
const connLabel      = document.getElementById('conn-label');
const gpsLabel       = document.getElementById('gps-label');
const fpsLabel       = document.getElementById('fps-label');
const setupPanel     = document.getElementById('setup-panel');
const toggleBtn      = document.getElementById('toggle-setup');
const btnStart       = document.getElementById('btn-start');
const phoneSelect    = document.getElementById('phone-model');
const fovSlider      = document.getElementById('fov-slider');
const fovDisplay     = document.getElementById('fov-value');
const orientPrompt   = document.getElementById('orientation-prompt');
const btnOrientPerm  = document.getElementById('btn-orient-perm');

// ── FOV setup ─────────────────────────────────────────────────────────────────
(function initFOV() {
    PHONE_FOV_DATABASE.forEach((entry, i) => {
        const opt = document.createElement('option');
        opt.value = i;
        opt.textContent = entry.label;
        phoneSelect.appendChild(opt);
    });

    // Restore from localStorage
    const saved = localStorage.getItem('phonevoxel_model_idx');
    if (saved !== null) {
        phoneSelect.value = saved;
        const entry = PHONE_FOV_DATABASE[parseInt(saved, 10)];
        if (entry && entry.hfov !== null) {
            currentHFOV = entry.hfov;
            fovSlider.value = entry.hfov;
            fovDisplay.textContent = entry.hfov.toFixed(1) + '°';
        }
    } else {
        fovSlider.value = currentHFOV;
        fovDisplay.textContent = currentHFOV.toFixed(1) + '°';
    }

    phoneSelect.addEventListener('change', e => {
        const idx = parseInt(e.target.value, 10);
        const entry = PHONE_FOV_DATABASE[idx];
        if (entry && entry.hfov !== null) {
            currentHFOV = entry.hfov;
            fovSlider.value = entry.hfov;
            fovDisplay.textContent = entry.hfov.toFixed(1) + '°';
        }
        localStorage.setItem('phonevoxel_model_idx', idx);
    });

    fovSlider.addEventListener('input', e => {
        currentHFOV = parseFloat(e.target.value);
        fovDisplay.textContent = currentHFOV.toFixed(1) + '°';
        // Reset dropdown to "Custom" if value doesn't match any preset
        const matchIdx = PHONE_FOV_DATABASE.findIndex(
            p => p.hfov !== null && Math.abs(p.hfov - currentHFOV) < 0.5
        );
        if (matchIdx === -1) {
            phoneSelect.value = PHONE_FOV_DATABASE.length - 1;
        }
    });
})();

// ── GPS ───────────────────────────────────────────────────────────────────────
function startGPS() {
    if (!navigator.geolocation) return;
    gpsWatchId = navigator.geolocation.watchPosition(
        pos => {
            currentPosition = {
                lat:      pos.coords.latitude,
                lon:      pos.coords.longitude,
                altitude: pos.coords.altitude || 0,
                accuracy: pos.coords.accuracy,
            };
            gpsLabel.textContent = `GPS: ±${pos.coords.accuracy.toFixed(0)}m`;
        },
        err => {
            gpsLabel.textContent = `GPS: err ${err.code}`;
        },
        { enableHighAccuracy: true, maximumAge: 0 }
    );
}

// ── Device orientation ────────────────────────────────────────────────────────
function attachOrientationListener() {
    window.addEventListener('deviceorientation', e => {
        currentOrientation = {
            alpha: e.alpha || 0,
            beta:  e.beta  || 0,
            gamma: e.gamma || 0,
        };
    });
}

async function requestOrientationPermission() {
    if (typeof DeviceOrientationEvent !== 'undefined' &&
        typeof DeviceOrientationEvent.requestPermission === 'function') {
        const perm = await DeviceOrientationEvent.requestPermission();
        if (perm !== 'granted') {
            alert('Orientation permission denied — heading will default to 0°.');
            return false;
        }
    }
    attachOrientationListener();
    return true;
}

// ── Camera ────────────────────────────────────────────────────────────────────
async function startCamera() {
    const constraints = {
        video: {
            facingMode: 'environment',
            width:     { ideal: CAP_WIDTH },
            height:    { ideal: Math.round(CAP_WIDTH * 0.75) },
            frameRate: { ideal: 15 },
        }
    };
    const stream = await navigator.mediaDevices.getUserMedia(constraints);
    video.srcObject = stream;
    await new Promise(resolve => { video.onloadedmetadata = resolve; });
    capCanvas.width  = video.videoWidth  || CAP_WIDTH;
    capCanvas.height = video.videoHeight || Math.round(CAP_WIDTH * 0.75);
}

// ── WebSocket ─────────────────────────────────────────────────────────────────
function setConnState(state) {
    connDot.className = 'dot ' + state;
    const labels = { green: 'Streaming', yellow: 'Connecting…', red: 'Disconnected' };
    connLabel.textContent = labels[state] || state;
}

let framesSent = 0;
let fpsInterval = null;

function openWS() {
    setConnState('yellow');
    ws = new WebSocket(SERVER_URL);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
        setConnState('green');
        startSendLoop();
        fpsInterval = setInterval(() => {
            fpsLabel.textContent = framesSent + ' fps';
            framesSent = 0;
        }, 1000);
    };

    ws.onclose = () => {
        setConnState('red');
        stopSendLoop();
        clearInterval(fpsInterval);
        // Auto-reconnect after 3s
        if (streaming) setTimeout(openWS, 3000);
    };

    ws.onerror = () => {
        ws.close();
    };

    ws.onmessage = e => {
        // Welcome message with our client ID
        try {
            const msg = JSON.parse(e.data);
            if (msg.type === 'welcome') {
                connLabel.textContent = 'Streaming (' + msg.client_id + ')';
            }
        } catch(_) {}
    };
}

// ── Send loop ─────────────────────────────────────────────────────────────────
const HEADER_SIZE = 84;

function buildMessage(pos, orient, jpegBuf) {
    const view = new DataView(new ArrayBuffer(HEADER_SIZE + jpegBuf.byteLength));
    const f64  = (offset, val) => view.setFloat64(offset, val, true);
    const u32  = (offset, val) => view.setUint32(offset, val, true);

    const ts = performance.now() / 1000 + tsOffset;
    f64(0,  ts);
    f64(8,  pos.lat);
    f64(16, pos.lon);
    f64(24, pos.altitude);
    f64(32, pos.accuracy);
    f64(40, orient.alpha);
    f64(48, orient.beta);
    f64(56, orient.gamma);
    f64(64, currentHFOV);
    u32(72, capCanvas.width);
    u32(76, capCanvas.height);
    u32(80, jpegBuf.byteLength);

    new Uint8Array(view.buffer, HEADER_SIZE).set(new Uint8Array(jpegBuf));
    return view.buffer;
}

function startSendLoop() {
    if (sendTimer) return;
    sendTimer = setInterval(() => {
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        if (!currentPosition) return;
        if (!currentOrientation) {
            // Use a zero orientation if sensor not yet available
            currentOrientation = { alpha: 0, beta: 0, gamma: 0 };
        }

        capCtx.drawImage(video, 0, 0, capCanvas.width, capCanvas.height);
        capCanvas.toBlob(blob => {
            if (!blob) return;
            blob.arrayBuffer().then(buf => {
                const msg = buildMessage(currentPosition, currentOrientation, buf);
                try {
                    ws.send(msg);
                    framesSent++;
                } catch(_) {}
            });
        }, 'image/jpeg', JPEG_QUALITY);
    }, SEND_RATE_MS);
}

function stopSendLoop() {
    if (sendTimer) { clearInterval(sendTimer); sendTimer = null; }
}

// ── Start button ──────────────────────────────────────────────────────────────
btnStart.addEventListener('click', async () => {
    btnStart.disabled = true;
    btnStart.textContent = 'Starting…';

    // 1. Camera
    try {
        await startCamera();
    } catch(err) {
        alert('Camera error: ' + err.message);
        btnStart.disabled = false;
        btnStart.textContent = 'Start Streaming';
        return;
    }

    // 2. GPS
    startGPS();

    // 3. Orientation — iOS requires permission from a user gesture
    if (typeof DeviceOrientationEvent !== 'undefined' &&
        typeof DeviceOrientationEvent.requestPermission === 'function') {
        // Show the dedicated prompt so user can re-trigger from a gesture
        orientPrompt.style.display = 'flex';
        return; // btnOrientPerm continues the flow below
    }

    attachOrientationListener();
    finishStart();
});

btnOrientPerm.addEventListener('click', async () => {
    orientPrompt.style.display = 'none';
    await requestOrientationPermission();
    finishStart();
});

function finishStart() {
    streaming = true;

    // Collapse setup panel, show toggle button
    setupPanel.style.display = 'none';
    toggleBtn.style.display  = 'block';

    // Open WebSocket
    openWS();
}

toggleBtn.addEventListener('click', () => {
    if (setupPanel.style.display === 'none') {
        setupPanel.style.display = 'block';
        toggleBtn.textContent = '▼ FOV';
    } else {
        setupPanel.style.display = 'none';
        toggleBtn.textContent = '▲ FOV';
    }
});
