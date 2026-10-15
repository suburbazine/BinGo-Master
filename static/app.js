// SIM2K-260 Web Architecture & Calibration Editor (Offline Single-Page App)

document.addEventListener('DOMContentLoaded', () => {
    initNavigation();
    initDashboard();
    initMaps();
    initDFlash();
    initRAM();
    initDIDs();
    initDisasm();
    initDiffsAndExport();
    initBinSelector();
});

// Helper for formatting hex
function toHex(val, padding = 8) {
    if (typeof val === 'string' && val.startsWith('0x')) return val;
    return '0x' + (Number(val) >>> 0).toString(16).toUpperCase().padStart(padding, '0');
}

function escapeHtml(str) {
    if (!str) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#039;');
}

/* =========================================================================
   1. NAVIGATION
   ========================================================================= */
function initNavigation() {
    const tabs = document.querySelectorAll('.nav-tab');
    const contents = document.querySelectorAll('.tab-content');

    tabs.forEach(tab => {
        tab.addEventListener('click', () => {
            const targetId = tab.getAttribute('data-tab');
            tabs.forEach(t => t.classList.remove('active'));
            contents.forEach(c => c.classList.remove('active'));

            tab.classList.add('active');
            const target = document.getElementById(`tab-${targetId}`);
            if (target) target.classList.add('active');

            // Trigger tab-specific refresh if needed
            if (targetId === 'dashboard') loadDashboard();
            if (targetId === 'dflash') loadDFlash();
            if (targetId === 'dids') loadDIDs();
            if (targetId === 'ram') loadCuratedRAM();
        });
    });

    // Modal close buttons
    document.querySelectorAll('.modal-close').forEach(btn => {
        btn.addEventListener('click', () => {
            btn.closest('.modal').classList.remove('active');
        });
    });
}

function switchToTab(tabName) {
    const tabBtn = document.querySelector(`.nav-tab[data-tab="${tabName}"]`);
    if (tabBtn) tabBtn.click();
}

/* =========================================================================
   2. DASHBOARD
   ========================================================================= */
function initDashboard() {
    loadDashboard();
}

async function loadDashboard() {
    try {
        const [statsRes, partsRes, matrixRes] = await Promise.all([
            fetch('/api/stats').then(r => r.json()),
            fetch('/api/partitions').then(r => r.json()),
            fetch('/api/matrix').then(r => r.json())
        ]);

        // Render Stats
        document.getElementById('stat-insns').innerText = statsRes.instruction_count.toLocaleString();
        document.getElementById('stat-functions').innerText = statsRes.function_count.toLocaleString();
        document.getElementById('stat-calls').innerText = statsRes.call_count.toLocaleString();
        document.getElementById('stat-mem').innerText = statsRes.memory_interactions.toLocaleString();
        document.getElementById('stat-maps').innerText = statsRes.map_count.toLocaleString();
        document.getElementById('stat-dflash').innerText = statsRes.dflash_block_count.toLocaleString();

        const liveMapsEl = document.getElementById('stat-live-maps');
        if (liveMapsEl) liveMapsEl.innerText = (statsRes.live_map_count || 0).toLocaleString();

        const deadMapsEl = document.getElementById('stat-dead-maps');
        if (deadMapsEl) deadMapsEl.innerText = (statsRes.dead_map_count || 0).toLocaleString();

        const isAWD = Array.isArray(partsRes) && partsRes.some(p => p.name === 'CAL_DPP1');
        const isTCU = Array.isArray(partsRes) && partsRes.some(p => p.name === 'CAL_HEADER_DIR');
        const deadLblEl = document.getElementById('stat-dead-maps-lbl');
        const deadMetaEl = document.getElementById('stat-dead-maps-meta');
        if (deadLblEl) deadLblEl.innerText = (isAWD || isTCU) ? 'Identity / Static Maps' : 'Inapplicable / Dead Maps';
        if (deadMetaEl) deadMetaEl.innerText = isTCU ? 'ECU IDs, SVN build tags & header descriptors' : (isAWD ? 'ECU IDs, headers & static blocks' : 'Optimal spark & unreferenced');

        const partBadgeEl = document.getElementById('stat-partitions-badge');
        if (partBadgeEl) partBadgeEl.innerText = `${statsRes.partition_count || partsRes.length} Partitions`;

        const hdrPartTitle = document.getElementById('hdr-partitions-title');
        if (hdrPartTitle) {
            hdrPartTitle.innerText = isTCU
                ? 'Kefico CPTSH 8AT TCU Big-Endian Flash & Calibration Partitions (A8RE25R1 / 95441-4J571)'
                : (isAWD
                    ? 'Infineon XC2000 / C166 Flash & RAM Partitions (DPP0..DPP3)'
                    : 'TC1797 / TC277 Flash Partitions & Memory Architecture');
        }

        // Sync Calibration Maps Applicability Filter Pills
        const setTxt = (id, val) => { const el = document.getElementById(id); if (el) el.innerText = val; };
        setTxt('pill-cnt-all', (statsRes.map_count || 0).toLocaleString());
        setTxt('pill-cnt-live', (statsRes.live_map_count || 0).toLocaleString());
        setTxt('pill-cnt-dead', (statsRes.dead_map_count || 0).toLocaleString());
        setTxt('pill-cnt-torque', (statsRes.torque_map_count || 0).toLocaleString());
        setTxt('pill-cnt-gated', (statsRes.gated_map_count || 0).toLocaleString());
        setTxt('pill-lbl-dead', (isAWD || isTCU) ? 'Identity / Static' : 'Inapplicable / Dead');
        setTxt('pill-lbl-torque', isAWD ? 'Breakpoint Axes' : 'Torque Ref');
        setTxt('pill-lbl-gated', (isAWD || isTCU) ? 'Diagnostic / DTC' : 'Gated');

        // Render Partitions Table
        const partTbody = document.querySelector('#table-partitions tbody');
        partTbody.innerHTML = partsRes.map(p => `
            <tr>
                <td class="mono">${p.id}</td>
                <td><strong>${escapeHtml(p.name)}</strong></td>
                <td class="mono">${p.start_ea_hex} .. ${p.end_ea_hex}</td>
                <td><span class="badge info">${escapeHtml(p.memory_type)}</span></td>
                <td class="mono">${p.size_kb > 0 ? p.size_kb.toLocaleString() + ' KB' : '-'}</td>
                <td class="mono">${p.fn_count.toLocaleString()}</td>
                <td class="mono">${p.insn_count.toLocaleString()}</td>
                <td style="color:var(--text-muted);">${escapeHtml(p.description || '')}</td>
            </tr>
        `).join('');

        // Sync Disassembly Partition Filter Dropdown
        const partSelect = document.getElementById('fn-partition-filter');
        if (partSelect && Array.isArray(partsRes)) {
            const curVal = partSelect.value;
            partSelect.innerHTML = `<option value="ALL">All Partitions</option>` +
                partsRes.map(p => `<option value="${escapeHtml(p.name)}">${escapeHtml(p.name)} (${escapeHtml(p.memory_type || '')})</option>`).join('');
            if (partsRes.some(p => p.name === curVal)) {
                partSelect.value = curVal;
            } else {
                partSelect.value = 'ALL';
            }
        }

        // Render Matrix
        renderMatrix(partsRes, matrixRes);
    } catch (err) {
        console.error('Failed to load dashboard data:', err);
    }
}

function renderMatrix(partitions, matrixData) {
    const wrapper = document.getElementById('matrix-wrapper');
    const matrixMap = {};
    matrixData.forEach(c => {
        matrixMap[`${c.source_partition}->${c.target_partition}`] = c.count;
    });

    const partNames = partitions.map(p => p.name);

    let html = '<table class="matrix-table"><thead><tr><th class="row-hdr">Source \\ Target</th>';
    partNames.forEach(name => {
        html += `<th title="${name}">${escapeHtml(name)}</th>`;
    });
    html += '</tr></thead><tbody>';

    partNames.forEach(src => {
        html += `<tr><th class="row-hdr" title="${src}">${escapeHtml(src)}</th>`;
        partNames.forEach(dst => {
            const cnt = matrixMap[`${src}->${dst}`] || 0;
            const cls = cnt > 1000 ? 'matrix-cell high-val' : cnt > 0 ? 'matrix-cell has-val' : 'matrix-cell zero';
            html += `<td class="${cls}" data-src="${src}" data-dst="${dst}" data-cnt="${cnt}" title="${src} -> ${dst}: ${cnt.toLocaleString()} interactions">${cnt > 0 ? cnt.toLocaleString() : '-'}</td>`;
        });
        html += '</tr>';
    });
    html += '</tbody></table>';

    wrapper.innerHTML = html;

    // Click handler for matrix cells
    wrapper.querySelectorAll('.matrix-cell.has-val, .matrix-cell.high-val').forEach(cell => {
        cell.addEventListener('click', () => {
            const src = cell.getAttribute('data-src');
            const dst = cell.getAttribute('data-dst');
            openMatrixTraffic(src, dst);
        });
    });
}

async function openMatrixTraffic(src, dst) {
    const modal = document.getElementById('modal-matrix-traffic');
    const title = document.getElementById('modal-traffic-title');
    const tbody = document.querySelector('#table-matrix-traffic tbody');

    title.innerText = `Boundary Interactions: ${src} -> ${dst}`;
    tbody.innerHTML = '<tr><td colspan="7" class="loading">Loading interactions...</td></tr>';
    modal.classList.add('active');

    try {
        const res = await fetch(`/api/matrix/interactions?src=${encodeURIComponent(src)}&dst=${encodeURIComponent(dst)}&limit=100`).then(r => r.json());
        if (!res.interactions || res.interactions.length === 0) {
            tbody.innerHTML = '<tr><td colspan="7">No interactions recorded.</td></tr>';
            return;
        }

        tbody.innerHTML = res.interactions.map(mi => `
            <tr>
                <td class="mono">${mi.insn_ea_hex}</td>
                <td><a href="#" class="ref-link" onclick="jumpToFunction('${mi.function_ea_hex}'); return false;">${escapeHtml(mi.function_name || mi.function_ea_hex)}</a></td>
                <td class="mono">${mi.target_ea_hex}</td>
                <td>${escapeHtml(mi.target_symbol || '-')}</td>
                <td><span class="badge ${mi.access_type === 'WRITE' ? 'warn' : 'info'}">${escapeHtml(mi.access_type)}</span></td>
                <td class="mono">${mi.access_width}B</td>
                <td class="mono">${escapeHtml(mi.mnemonic)} ${escapeHtml(mi.op_str)}</td>
            </tr>
        `).join('');
    } catch (err) {
        tbody.innerHTML = `<tr><td colspan="7" style="color:var(--red);">Error loading traffic: ${escapeHtml(err.message)}</td></tr>`;
    }
}

/* =========================================================================
   3. CALIBRATION MAPS
   ========================================================================= */
let currentMapObj = null;
let currentMapData = null;
let pendingEdits = [];

function getApplicabilityBadge(app, isDetailed = false) {
    const idAttr = isDetailed ? ' id="cur-map-app"' : '';
    switch (app) {
        case 'LIVE_ACTUATOR':
            return `<span${idAttr} class="badge badge-live" title="Live physical actuator">${isDetailed ? 'LIVE ACTUATOR' : 'LIVE'}</span>`;
        case 'TORQUE_MODEL_REF':
            return `<span${idAttr} class="badge badge-tq" title="Internal torque model reference. Does NOT advance coils.">${isDetailed ? 'TORQUE MODEL ONLY' : 'TORQUE MODEL'}</span>`;
        case 'AXIS':
            return `<span${idAttr} class="badge badge-tq" title="1D Breakpoint Axis referenced by 1D/2D maps">${isDetailed ? 'BREAKPOINT AXIS' : 'AXIS'}</span>`;
        case 'GATED_DISABLED':
            return `<span${idAttr} class="badge badge-gated" title="Gated / bypassed by zero scale or switch">${isDetailed ? 'GATED / DISABLED' : 'GATED'}</span>`;
        case 'DIAGNOSTIC':
            return `<span${idAttr} class="badge badge-gated" title="UDS / DTC / RTOS diagnostic descriptor table">${isDetailed ? 'DIAGNOSTIC TABLE' : 'DIAG'}</span>`;
        case 'DEAD_INACTIVE':
            return `<span${idAttr} class="badge badge-dead" title="Unreferenced / hardware incompatible">${isDetailed ? 'DEAD / INACTIVE' : 'DEAD'}</span>`;
        case 'IDENTITY':
            return `<span${idAttr} class="badge badge-dead" title="ECU identity string / header constant">${isDetailed ? 'IDENTITY / HEADER' : 'IDENTITY'}</span>`;
        default:
            return `<span${idAttr} class="badge info">${escapeHtml(app || 'UNKNOWN')}</span>`;
    }
}

function initMaps() {
    const searchInput = document.getElementById('map-search');
    const typeFilter = document.getElementById('map-type-filter');
    const appFilter = document.getElementById('map-app-filter');
    const clearBtn = document.getElementById('btn-clear-map-search');

    // Wire up Applicability Pills
    document.querySelectorAll('#map-app-pills .app-pill').forEach(pill => {
        pill.addEventListener('click', () => {
            document.querySelectorAll('#map-app-pills .app-pill').forEach(p => p.classList.remove('active'));
            pill.classList.add('active');
            const appVal = pill.getAttribute('data-app');
            const hiddenInput = document.getElementById('map-app-filter');
            if (hiddenInput) hiddenInput.value = appVal;
            searchMaps();
        });
    });

    let debounceTimer = null;
    searchInput.addEventListener('input', () => {
        clearTimeout(debounceTimer);
        debounceTimer = setTimeout(() => searchMaps(), 250);
    });

    typeFilter.addEventListener('change', () => searchMaps());
    if (appFilter) appFilter.addEventListener('change', () => searchMaps());

    clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        searchMaps();
    });

    document.querySelectorAll('input[name="map-view-mode"]').forEach(radio => {
        radio.addEventListener('change', () => {
            syncMapViewMode();
            if (currentMapData) renderMapGrid(currentMapData);
        });
    });

    document.querySelectorAll('input[name="val-display"]').forEach(radio => {
        radio.addEventListener('change', () => {
            if (currentMapData) renderMapGrid(currentMapData);
        });
    });

    const heatmapToggle = document.getElementById('toggle-heatmap');
    if (heatmapToggle) {
        heatmapToggle.addEventListener('change', () => {
            if (currentMapData) renderMapGrid(currentMapData);
        });
    }

    initSurface3D();

    document.getElementById('btn-reset-map').addEventListener('click', async () => {
        if (!currentMapObj) return;
        if (confirm(`Revert all unsaved edits for map "${currentMapObj.name}"?`)) {
            await fetch(`/api/maps/${currentMapObj.id}/reset`, { method: 'POST' });
            loadMapDetail(currentMapObj.id);
            updateDiffCount();
        }
    });

    document.getElementById('btn-save-map').addEventListener('click', async () => {
        if (!currentMapObj || pendingEdits.length === 0) {
            alert('No pending cell edits to apply.');
            return;
        }

        try {
            const res = await fetch(`/api/maps/${currentMapObj.id}/edit`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ map_id: currentMapObj.id, edits: pendingEdits })
            }).then(r => r.json());

            if (res.success) {
                pendingEdits = [];
                loadMapDetail(currentMapObj.id);
                updateDiffCount();
                alert(`Successfully applied edits to in-memory calibration session.`);
            }
        } catch (err) {
            alert(`Failed to save edits: ${err.message}`);
        }
    });

    // Cell Edit Modal Handlers
    initCellEditModal();

    searchMaps();
}

function getActiveMapViewMode() {
    const checked = document.querySelector('input[name="map-view-mode"]:checked');
    return checked ? checked.value : 'grid';
}

function syncMapViewMode() {
    const mode = getActiveMapViewMode();
    const gridWrap = document.getElementById('map-grid-wrapper');
    const surfWrap = document.getElementById('map-surface3d-wrapper');
    if (gridWrap) gridWrap.style.display = mode === 'grid' ? 'block' : 'none';
    if (surfWrap) surfWrap.style.display = mode === 'surface3d' ? 'flex' : 'none';
}

function filterMapsByApp(appVal) {
    switchToTab('maps');
    const hiddenInput = document.getElementById('map-app-filter');
    if (hiddenInput) hiddenInput.value = appVal;
    document.querySelectorAll('#map-app-pills .app-pill').forEach(p => {
        if (p.getAttribute('data-app') === appVal) {
            p.classList.add('active');
        } else {
            p.classList.remove('active');
        }
    });
    searchMaps();
}

async function searchMaps() {
    const q = document.getElementById('map-search').value.trim();
    const type = document.getElementById('map-type-filter').value;
    const app = document.getElementById('map-app-filter') ? document.getElementById('map-app-filter').value : 'ALL';
    const listEl = document.getElementById('map-list');
    const countBadge = document.getElementById('map-count-badge');

    listEl.innerHTML = '<div class="loading">Searching maps...</div>';

    try {
        const res = await fetch(`/api/maps?q=${encodeURIComponent(q)}&type=${encodeURIComponent(type)}&applicability=${encodeURIComponent(app)}&limit=100`).then(r => r.json());
        countBadge.innerText = `${res.total.toLocaleString()} maps found`;

        if (!res.maps || res.maps.length === 0) {
            listEl.innerHTML = '<div class="empty-state"><p>No calibration maps found.</p></div>';
            return;
        }

        listEl.innerHTML = res.maps.map(m => `
            <div class="list-item ${currentMapObj && currentMapObj.id === m.id ? 'selected' : ''}" data-id="${m.id}">
                <div class="list-item-title">
                    <span style="display:flex; align-items:center; gap:6px; overflow:hidden; text-overflow:ellipsis;">
                        ${getApplicabilityBadge(m.applicability)}
                        ${escapeHtml(m.name)}
                    </span>
                    <span class="badge info">${escapeHtml(m.map_type)}</span>
                </div>
                <div class="list-item-meta">
                    ${m.file_offset_hex} | ${m.columns}x${m.rows} ${m.data_org} | ${escapeHtml(m.unit || '-')}
                </div>
            </div>
        `).join('');

        listEl.querySelectorAll('.list-item').forEach(item => {
            item.addEventListener('click', () => {
                const id = parseInt(item.getAttribute('data-id'), 10);
                listEl.querySelectorAll('.list-item').forEach(i => i.classList.remove('selected'));
                item.classList.add('selected');
                loadMapDetail(id);
            });
        });
    } catch (err) {
        listEl.innerHTML = `<div class="empty-state" style="color:var(--red);">Error: ${escapeHtml(err.message)}</div>`;
    }
}

async function loadMapDetail(mapID) {
    const placeholder = document.getElementById('map-editor-placeholder');
    const content = document.getElementById('map-editor-content');

    placeholder.style.display = 'none';
    content.style.display = 'flex';

    try {
        const res = await fetch(`/api/maps/${mapID}`).then(r => r.json());
        currentMapObj = res.map;
        currentMapData = res;
        pendingEdits = [];

        // Meta headers
        document.getElementById('cur-map-name').innerText = currentMapObj.name;
        document.getElementById('cur-map-folder').innerText = currentMapObj.folder || 'Root';
        document.getElementById('cur-map-offset').innerText = `File: ${currentMapObj.file_offset_hex}`;
        document.getElementById('cur-map-ea').innerText = `EA: ${currentMapObj.ea_hex}`;
        document.getElementById('cur-map-dims').innerText = `${currentMapObj.columns}x${currentMapObj.rows} (${currentMapObj.map_type})`;
        document.getElementById('cur-map-org').innerText = `Org: ${currentMapObj.data_org}`;
        document.getElementById('cur-map-unit').innerText = `Unit: ${currentMapObj.unit || '-'}`;

        // Applicability Badge & Guidance Note
        const appBadgeEl = document.getElementById('cur-map-app');
        if (appBadgeEl) {
            appBadgeEl.outerHTML = getApplicabilityBadge(currentMapObj.applicability, true);
        }

        const appNoteEl = document.getElementById('cur-map-app-note');
        if (appNoteEl) {
            const app = currentMapObj.applicability || 'UNKNOWN';
            if (app === 'DEAD_INACTIVE') {
                appNoteEl.innerHTML = `
                    <div style="font-weight:700; margin-bottom: 2px;">⚠️ INAPPLICABLE / DEAD CALIBRATION MAP (DOES NOT APPLY TO THIS ECU)</div>
                    <div>${escapeHtml(currentMapObj.applicability_note || 'This map (e.g. optimal spark angle / unreferenced subsystem) is not modulated by firmware on this SIM2K-260 family. Modifying this table will have zero physical effect on engine operation.')}</div>
                `;
                appNoteEl.className = 'map-app-alert alert-dead_inactive';
                appNoteEl.style.display = 'block';
            } else if (currentMapObj.applicability_note) {
                appNoteEl.innerHTML = `<strong>Applicability Guidance:</strong> ${escapeHtml(currentMapObj.applicability_note)}`;
                appNoteEl.className = `map-app-alert alert-${app.toLowerCase()}`;
                appNoteEl.style.display = 'block';
            } else {
                appNoteEl.style.display = 'none';
            }
        }

        // Ref functions
        const refsEl = document.getElementById('cur-map-refs');
        if (currentMapObj.ref_fn_list && currentMapObj.ref_fn_list.length > 0) {
            refsEl.innerHTML = '<span>Referenced by Functions:</span> ' + currentMapObj.ref_fn_list.map(fn => {
                const eaMatch = fn.match(/fn_([0-9A-Fa-f]+)/);
                const eaHex = eaMatch ? '0x' + eaMatch[1] : '';
                return `<button class="ref-link" onclick="jumpToFunction('${eaHex}'); return false;">${escapeHtml(fn)}</button>`;
            }).join(' ');
            refsEl.style.display = 'flex';
        } else {
            refsEl.style.display = 'none';
        }

        // Comment
        const commentEl = document.getElementById('cur-map-comment');
        if (currentMapObj.comment) {
            commentEl.innerHTML = `<strong>Map Notes / Traced Logic:</strong> ${escapeHtml(currentMapObj.comment)}`;
            commentEl.style.display = 'block';
        } else {
            commentEl.style.display = 'none';
        }

        syncMapViewMode();
        renderMapGrid(res);
    } catch (err) {

        console.error('Failed to load map detail:', err);
    }
}

function renderMapGrid(data) {
    const grid = document.getElementById('map-grid');
    const showPhysical = document.querySelector('input[name="val-display"]:checked').value === 'physical';
    const heatmapToggle = document.getElementById('toggle-heatmap');
    const useHeatmap = heatmapToggle ? heatmapToggle.checked : true;

    const cols = data.map.columns || 1;
    const rows = data.map.rows || 1;

    // 1. Calculate Min and Max across all cells for scaling
    let minVal = Infinity;
    let maxVal = -Infinity;
    for (let r = 0; r < rows; r++) {
        for (let c = 0; c < cols; c++) {
            const v = showPhysical ? data.cells[r][c].physical : data.cells[r][c].raw;
            if (typeof v === 'number' && !isNaN(v)) {
                if (v < minVal) minVal = v;
                if (v > maxVal) maxVal = v;
            }
        }
    }
    if (!isFinite(minVal)) minVal = 0;
    if (!isFinite(maxVal)) maxVal = 0;

    // 2. Render Heatmap Legend Bar
    const legendEl = document.getElementById('map-heatmap-legend');
    if (legendEl) {
        if (useHeatmap && maxVal > minVal) {
            legendEl.style.display = 'flex';
            const unitStr = data.map.unit ? ` ${escapeHtml(data.map.unit)}` : '';
            const minStr = Number.isInteger(minVal) ? minVal : minVal.toFixed(2);
            const maxStr = Number.isInteger(maxVal) ? maxVal : maxVal.toFixed(2);
            legendEl.innerHTML = `
                <span class="heatmap-title">Rainbow Scale:</span>
                <span class="heatmap-lbl" style="color:#60a5fa;">${minStr}${unitStr}</span>
                <div class="heatmap-bar"></div>
                <span class="heatmap-lbl" style="color:#f87171;">${maxStr}${unitStr}</span>
            `;
        } else {
            legendEl.style.display = 'none';
        }
    }

    // 3. Build Table Headers
    let html = '<thead><tr><th>Y \\ X</th>';
    for (let c = 0; c < cols; c++) {
        const xPt = data.x_axis && data.x_axis[c] ? data.x_axis[c].label : `Col ${c}`;
        html += `<th>${escapeHtml(xPt)}</th>`;
    }
    html += '</tr></thead><tbody>';

    // 4. Build Table Rows & Rainbow Cells
    for (let r = 0; r < rows; r++) {
        const yPt = data.y_axis && data.y_axis[r] ? data.y_axis[r].label : `Row ${r}`;
        html += `<tr><th>${escapeHtml(yPt)}</th>`;
        for (let c = 0; c < cols; c++) {
            const cell = data.cells[r][c];
            const val = showPhysical ? cell.physical : cell.raw;
            let cls = cell.is_modified ? 'modified' : '';

            let styleAttr = '';
            if (useHeatmap) {
                if (maxVal > minVal) {
                    const ratio = Math.max(0, Math.min(1, (val - minVal) / (maxVal - minVal)));
                    // Rainbow hue mapping: 240 (blue) down to 0 (red)
                    const hue = (1.0 - ratio) * 240;
                    styleAttr = `style="background: hsla(${hue.toFixed(1)}, 82%, 35%, 0.65); border: 1px solid hsla(${hue.toFixed(1)}, 88%, 52%, 0.35); color: #ffffff; text-shadow: 0 1px 2px rgba(0,0,0,0.9);"`;
                } else {
                    styleAttr = `style="background: hsla(210, 75%, 32%, 0.45); border: 1px solid hsla(210, 80%, 48%, 0.3); color: #ffffff;"`;
                }
            }

            const formattedVal = (typeof val === 'number' && !Number.isInteger(val)) ? val.toFixed(2) : val;
            const title = `[R:${r}, C:${c}] Raw: ${cell.raw} (0x${cell.raw.toString(16).toUpperCase()}), Phys: ${cell.physical} ${data.map.unit}`;
            html += `<td class="${cls}" data-row="${r}" data-col="${c}" ${styleAttr} title="${title}">${formattedVal}</td>`;
        }
        html += '</tr>';
    }
    html += '</tbody>';

    grid.innerHTML = html;

    // Attach cell click
    grid.querySelectorAll('tbody td').forEach(td => {
        td.addEventListener('click', () => {
            const r = parseInt(td.getAttribute('data-row'), 10);
            const c = parseInt(td.getAttribute('data-col'), 10);
            openCellEditModal(r, c);
        });
    });

    if (getActiveMapViewMode() === 'surface3d') {
        requestAnimationFrame(() => renderSurface3D(data, minVal, maxVal, showPhysical, useHeatmap));
    }
}

/* =========================================================================
   3B. INTERACTIVE 3D SURFACE / WIREFRAME RENDERER
   ========================================================================= */
const surface3dState = {
    yaw: -0.68,
    pitch: 0.52,
    zoom: 1.0,
    panX: 0,
    panY: 0,
    isDragging: false,
    dragAction: 'rotate',
    dragStartX: 0,
    dragStartY: 0,
    movedDist: 0,
    hoveredVertex: null,
    projectedVertices: [],
    lastMinVal: 0,
    lastMaxVal: 0,
    lastShowPhys: true,
    lastUseHeatmap: true
};

function getSurface3dDragMode() {
    const checked = document.querySelector('input[name="surface3d-drag-mode"]:checked');
    return checked ? checked.value : 'rotate';
}

function initSurface3D() {
    const canvas = document.getElementById('map-surface3d-canvas');
    const resetBtn = document.getElementById('btn-3d-reset-cam');
    const wireToggle = document.getElementById('toggle-3d-wireframe');
    const tooltip = document.getElementById('surface3d-tooltip');
    if (!canvas) return;

    if (resetBtn) {
        resetBtn.addEventListener('click', () => {
            surface3dState.yaw = -0.68;
            surface3dState.pitch = 0.52;
            surface3dState.zoom = 1.0;
            surface3dState.panX = 0;
            surface3dState.panY = 0;
            if (currentMapData) renderMapGrid(currentMapData);
        });
    }

    if (wireToggle) {
        wireToggle.addEventListener('change', () => {
            if (currentMapData) renderMapGrid(currentMapData);
        });
    }

    document.querySelectorAll('input[name="surface3d-drag-mode"]').forEach(r => {
        r.addEventListener('change', () => {
            canvas.style.cursor = getSurface3dDragMode() === 'pan' ? 'move' : 'grab';
        });
    });

    canvas.addEventListener('contextmenu', (e) => {
        e.preventDefault();
    });

    canvas.addEventListener('mousedown', (e) => {
        surface3dState.isDragging = true;
        const isPanTrigger = (e.button === 2 || e.button === 1 || e.shiftKey || getSurface3dDragMode() === 'pan');
        surface3dState.dragAction = isPanTrigger ? 'pan' : 'rotate';
        surface3dState.dragStartX = e.clientX;
        surface3dState.dragStartY = e.clientY;
        surface3dState.movedDist = 0;
        if (isPanTrigger) {
            e.preventDefault();
        }
    });

    window.addEventListener('mousemove', (e) => {
        if (!currentMapData || getActiveMapViewMode() !== 'surface3d') return;

        if (surface3dState.isDragging) {
            const dx = e.clientX - surface3dState.dragStartX;
            const dy = e.clientY - surface3dState.dragStartY;
            surface3dState.movedDist += Math.hypot(dx, dy);
            surface3dState.dragStartX = e.clientX;
            surface3dState.dragStartY = e.clientY;

            if (surface3dState.dragAction === 'pan') {
                surface3dState.panX += dx;
                surface3dState.panY += dy;
            } else {
                surface3dState.yaw += dx * 0.0085;
                surface3dState.pitch = Math.max(0.08, Math.min(1.48, surface3dState.pitch + dy * 0.0085));
            }
            if (tooltip) tooltip.style.display = 'none';
            renderSurface3D(currentMapData, surface3dState.lastMinVal, surface3dState.lastMaxVal, surface3dState.lastShowPhys, surface3dState.lastUseHeatmap);
            return;
        }

        // Hover vertex detection
        const rect = canvas.getBoundingClientRect();
        const mx = e.clientX - rect.left;
        const my = e.clientY - rect.top;
        if (mx < 0 || my < 0 || mx > rect.width || my > rect.height) {
            if (surface3dState.hoveredVertex) {
                surface3dState.hoveredVertex = null;
                if (tooltip) tooltip.style.display = 'none';
                renderSurface3D(currentMapData, surface3dState.lastMinVal, surface3dState.lastMaxVal, surface3dState.lastShowPhys, surface3dState.lastUseHeatmap);
            }
            return;
        }

        let best = null;
        let bestDist = 16;
        for (const v of surface3dState.projectedVertices) {
            const d = Math.hypot(v.sx - mx, v.sy - my);
            if (d < bestDist) {
                bestDist = d;
                best = v;
            }
        }

        const prev = surface3dState.hoveredVertex;
        if ((best && !prev) || (!best && prev) || (best && prev && (best.r !== prev.r || best.c !== prev.c))) {
            surface3dState.hoveredVertex = best;
            renderSurface3D(currentMapData, surface3dState.lastMinVal, surface3dState.lastMaxVal, surface3dState.lastShowPhys, surface3dState.lastUseHeatmap);
        }

        if (best && tooltip) {
            const xLbl = currentMapData.x_axis && currentMapData.x_axis[best.c] ? currentMapData.x_axis[best.c].label : `Col ${best.c}`;
            const yLbl = currentMapData.y_axis && currentMapData.y_axis[best.r] ? currentMapData.y_axis[best.r].label : `Row ${best.r}`;
            const unitStr = currentMapData.map.unit ? ` ${escapeHtml(currentMapData.map.unit)}` : '';
            const modBadge = best.cell.is_modified ? ` <span style="color:#fbbf24;font-weight:700;">[EDITED]</span>` : '';
            tooltip.innerHTML = `
                <div style="color:var(--accent);font-weight:700;margin-bottom:2px;">[Row ${best.r}, Col ${best.c}]${modBadge}</div>
                <div>X (${escapeHtml(currentMapData.map.axis_x_name || 'Col')}): <strong>${escapeHtml(xLbl)}</strong></div>
                <div>Y (${escapeHtml(currentMapData.map.axis_y_name || 'Row')}): <strong>${escapeHtml(yLbl)}</strong></div>
                <div>Physical: <strong style="color:#6ee7b7;">${best.cell.physical}${unitStr}</strong></div>
                <div>Raw: <strong>${best.cell.raw}</strong> (0x${(best.cell.raw >>> 0).toString(16).toUpperCase()})</div>
                <div style="font-size:10px;color:var(--text-muted);margin-top:3px;">Click vertex to edit cell</div>
            `;
            tooltip.style.display = 'block';
            const tx = Math.min(rect.width - 210, Math.max(10, best.sx + 14));
            const ty = Math.min(rect.height - 115, Math.max(10, best.sy - 20));
            tooltip.style.left = `${tx}px`;
            tooltip.style.top = `${ty}px`;
        } else if (tooltip) {
            tooltip.style.display = 'none';
        }
    });

    window.addEventListener('mouseup', (e) => {
        if (!surface3dState.isDragging) return;
        const wasLeftClick = (e.button === 0);
        surface3dState.isDragging = false;
        if (wasLeftClick && surface3dState.movedDist < 6 && surface3dState.hoveredVertex) {
            openCellEditModal(surface3dState.hoveredVertex.r, surface3dState.hoveredVertex.c);
        }
    });

    canvas.addEventListener('wheel', (e) => {
        e.preventDefault();
        const rect = canvas.getBoundingClientRect();
        const mx = e.clientX - rect.left;
        const my = e.clientY - rect.top;
        const baseCx = rect.width * 0.5;
        const baseCy = rect.height * 0.5;

        const oldZoom = surface3dState.zoom;
        const factor = e.deltaY < 0 ? 1.12 : 0.89;
        const newZoom = Math.max(0.35, Math.min(4.5, oldZoom * factor));
        const zoomRatio = newZoom / oldZoom;

        // Anchor zoom around the mouse cursor position so the target region stays in view
        const curCx = baseCx + surface3dState.panX;
        const curCy = baseCy + surface3dState.panY;
        const nextCx = mx - (mx - curCx) * zoomRatio;
        const nextCy = my - (my - curCy) * zoomRatio;

        surface3dState.panX = nextCx - baseCx;
        surface3dState.panY = nextCy - baseCy;
        surface3dState.zoom = newZoom;

        if (currentMapData) {
            renderSurface3D(currentMapData, surface3dState.lastMinVal, surface3dState.lastMaxVal, surface3dState.lastShowPhys, surface3dState.lastUseHeatmap);
        }
    }, { passive: false });

    window.addEventListener('resize', () => {
        if (currentMapData && getActiveMapViewMode() === 'surface3d') {
            renderSurface3D(currentMapData, surface3dState.lastMinVal, surface3dState.lastMaxVal, surface3dState.lastShowPhys, surface3dState.lastUseHeatmap);
        }
    });
}

function renderSurface3D(data, minVal, maxVal, showPhysical, useHeatmap) {
    const canvas = document.getElementById('map-surface3d-canvas');
    if (!canvas || !data || !data.cells) return;

    surface3dState.lastMinVal = minVal;
    surface3dState.lastMaxVal = maxVal;
    surface3dState.lastShowPhys = showPhysical;
    surface3dState.lastUseHeatmap = useHeatmap;

    const box = canvas.parentElement;
    const width = box.clientWidth || 720;
    const height = box.clientHeight || 440;
    const dpr = window.devicePixelRatio || 1;
    if (canvas.width !== Math.floor(width * dpr) || canvas.height !== Math.floor(height * dpr)) {
        canvas.width = Math.floor(width * dpr);
        canvas.height = Math.floor(height * dpr);
    }

    const ctx = canvas.getContext('2d');
    ctx.save();
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, width, height);

    const cols = data.map.columns || 1;
    const rows = data.map.rows || 1;
    const wireToggle = document.getElementById('toggle-3d-wireframe');
    const showWire = wireToggle ? wireToggle.checked : true;

    const cosY = Math.cos(surface3dState.yaw);
    const sinY = Math.sin(surface3dState.yaw);
    const cosP = Math.cos(surface3dState.pitch);
    const sinP = Math.sin(surface3dState.pitch);

    const scale = Math.min(width, height) * 0.36 * surface3dState.zoom;
    const cx = width * 0.5 + surface3dState.panX;
    const cy = height * 0.5 + surface3dState.panY;
    const zHeightScale = 0.85;

    // Project normalized coordinates: nx in [-1, 1], ny in [-1, 1], nz in [0, 1]
    function project3D(nx, ny, nz) {
        const hz = (nz - 0.5) * zHeightScale;
        const x1 = nx * cosY - ny * sinY;
        const y1 = nx * sinY + ny * cosY;
        const sx = cx + x1 * scale;
        const sy = cy + (y1 * sinP - hz * cosP) * scale;
        const depth = y1 * cosP + hz * sinP;
        return { sx, sy, depth };
    }

    // 1. Draw 3D Base Floor & Bounding Box Axes
    const c00 = project3D(-1, -1, 0);
    const c10 = project3D(1, -1, 0);
    const c11 = project3D(1, 1, 0);
    const c01 = project3D(-1, 1, 0);
    const c00Top = project3D(-1, -1, 1);

    // Base floor fill
    ctx.beginPath();
    ctx.moveTo(c00.sx, c00.sy);
    ctx.lineTo(c10.sx, c10.sy);
    ctx.lineTo(c11.sx, c11.sy);
    ctx.lineTo(c01.sx, c01.sy);
    ctx.closePath();
    ctx.fillStyle = 'rgba(15, 23, 42, 0.55)';
    ctx.fill();
    ctx.strokeStyle = 'rgba(148, 163, 184, 0.28)';
    ctx.lineWidth = 1;
    ctx.stroke();

    // Base grid ticks (5 divisions)
    ctx.strokeStyle = 'rgba(148, 163, 184, 0.12)';
    for (let i = 1; i < 4; i++) {
        const t = -1 + (i / 4) * 2;
        const pA = project3D(t, -1, 0);
        const pB = project3D(t, 1, 0);
        ctx.beginPath();
        ctx.moveTo(pA.sx, pA.sy);
        ctx.lineTo(pB.sx, pB.sy);
        ctx.stroke();

        const qA = project3D(-1, t, 0);
        const qB = project3D(1, t, 0);
        ctx.beginPath();
        ctx.moveTo(qA.sx, qA.sy);
        ctx.lineTo(qB.sx, qB.sy);
        ctx.stroke();
    }

    // Vertical Z-axis pillar at (-1, -1)
    ctx.beginPath();
    ctx.moveTo(c00.sx, c00.sy);
    ctx.lineTo(c00Top.sx, c00Top.sy);
    ctx.strokeStyle = 'rgba(56, 189, 248, 0.55)';
    ctx.lineWidth = 1.5;
    ctx.stroke();

    ctx.font = '10px JetBrains Mono, monospace';
    ctx.fillStyle = '#94a3b8';
    const unitStr = data.map.unit ? ` ${data.map.unit}` : '';
    for (let zTick = 0; zTick <= 2; zTick++) {
        const zt = zTick / 2;
        const zp = project3D(-1, -1, zt);
        const zVal = minVal + zt * (maxVal - minVal);
        const zStr = Number.isInteger(zVal) ? String(zVal) : zVal.toFixed(1);
        ctx.fillText(`${zStr}${zTick === 2 ? unitStr : ''}`, zp.sx - 48, zp.sy + 3);
    }

    // X-Axis & Y-Axis edge labels
    const xMid = project3D(0, 1.16, 0);
    const yMid = project3D(1.16, 0, 0);
    const xFirst = data.x_axis && data.x_axis[0] ? data.x_axis[0].label : '0';
    const xLast = data.x_axis && data.x_axis[cols - 1] ? data.x_axis[cols - 1].label : String(cols - 1);
    const yFirst = data.y_axis && data.y_axis[0] ? data.y_axis[0].label : '0';
    const yLast = data.y_axis && data.y_axis[rows - 1] ? data.y_axis[rows - 1].label : String(rows - 1);

    ctx.fillStyle = '#38bdf8';
    ctx.fillText(`X: ${data.map.axis_x_name || 'Cols'} [${xFirst} .. ${xLast}]`, xMid.sx - 60, xMid.sy);
    ctx.fillStyle = '#a78bfa';
    ctx.fillText(`Y: ${data.map.axis_y_name || 'Rows'} [${yFirst} .. ${yLast}]`, yMid.sx + 6, yMid.sy);

    // 2. Compute 3D Grid Vertices
    const gridPts = [];
    const projList = [];
    const span = maxVal > minVal ? (maxVal - minVal) : 1;

    for (let r = 0; r < rows; r++) {
        gridPts[r] = [];
        const ny = rows > 1 ? -1 + (r / (rows - 1)) * 2 : 0;
        for (let c = 0; c < cols; c++) {
            const nx = cols > 1 ? -1 + (c / (cols - 1)) * 2 : 0;
            const cell = data.cells[r][c];
            const val = showPhysical ? cell.physical : cell.raw;
            const ratio = maxVal > minVal ? Math.max(0, Math.min(1, (val - minVal) / span)) : 0.5;
            const p = project3D(nx, ny, ratio);
            const vObj = { r, c, nx, ny, nz: ratio, val, ratio, sx: p.sx, sy: p.sy, depth: p.depth, cell };
            gridPts[r][c] = vObj;
            projList.push(vObj);
        }
    }
    surface3dState.projectedVertices = projList;

    // 3. Render Surface Quads (2D Map) or Extruded Ribbon (1D Curve)
    if (rows >= 2 && cols >= 2) {
        const quads = [];
        for (let r = 0; r < rows - 1; r++) {
            for (let c = 0; c < cols - 1; c++) {
                const v00 = gridPts[r][c];
                const v10 = gridPts[r][c + 1];
                const v11 = gridPts[r + 1][c + 1];
                const v01 = gridPts[r + 1][c];
                const avgDepth = (v00.depth + v10.depth + v11.depth + v01.depth) * 0.25;
                const avgRatio = (v00.ratio + v10.ratio + v11.ratio + v01.ratio) * 0.25;
                quads.push({ v00, v10, v11, v01, avgDepth, avgRatio });
            }
        }

        // Painter's algorithm: sort back-to-front (smaller depth drawn first)
        quads.sort((a, b) => a.avgDepth - b.avgDepth);

        for (const q of quads) {
            ctx.beginPath();
            ctx.moveTo(q.v00.sx, q.v00.sy);
            ctx.lineTo(q.v10.sx, q.v10.sy);
            ctx.lineTo(q.v11.sx, q.v11.sy);
            ctx.lineTo(q.v01.sx, q.v01.sy);
            ctx.closePath();

            if (useHeatmap) {
                const hue = (1.0 - q.avgRatio) * 240;
                ctx.fillStyle = `hsla(${hue.toFixed(1)}, 84%, 44%, 0.78)`;
                ctx.strokeStyle = showWire ? `hsla(${hue.toFixed(1)}, 92%, 68%, 0.65)` : `hsla(${hue.toFixed(1)}, 84%, 44%, 0.4)`;
            } else {
                ctx.fillStyle = 'rgba(30, 64, 175, 0.62)';
                ctx.strokeStyle = showWire ? 'rgba(96, 165, 250, 0.65)' : 'rgba(30, 64, 175, 0.35)';
            }
            ctx.lineWidth = showWire ? 1.1 : 0.5;
            ctx.fill();
            ctx.stroke();
        }
    } else {
        // 1D Curve or Scalar: draw extruded curtain to floor + glowing 3D line
        const pts = projList;
        if (pts.length >= 2) {
            for (let i = 0; i < pts.length - 1; i++) {
                const p0 = pts[i];
                const p1 = pts[i + 1];
                const b0 = project3D(p0.nx, p0.ny, 0);
                const b1 = project3D(p1.nx, p1.ny, 0);
                const avgRatio = (p0.ratio + p1.ratio) * 0.5;
                const hue = (1.0 - avgRatio) * 240;

                ctx.beginPath();
                ctx.moveTo(b0.sx, b0.sy);
                ctx.lineTo(p0.sx, p0.sy);
                ctx.lineTo(p1.sx, p1.sy);
                ctx.lineTo(b1.sx, b1.sy);
                ctx.closePath();
                ctx.fillStyle = useHeatmap ? `hsla(${hue.toFixed(1)}, 82%, 42%, 0.38)` : 'rgba(56, 189, 248, 0.25)';
                ctx.fill();

                ctx.beginPath();
                ctx.moveTo(p0.sx, p0.sy);
                ctx.lineTo(p1.sx, p1.sy);
                ctx.strokeStyle = useHeatmap ? `hsla(${hue.toFixed(1)}, 92%, 62%, 0.95)` : '#38bdf8';
                ctx.lineWidth = 2.5;
                ctx.stroke();
            }
        }
    }

    // 4. Draw Vertex Nodes (Modified cells in Gold, Hovered cell with Target Ring)
    const hov = surface3dState.hoveredVertex;
    for (const v of projList) {
        const isHov = hov && hov.r === v.r && hov.c === v.c;
        const isMod = v.cell.is_modified;
        if (!isHov && !isMod && projList.length > 400) continue;

        ctx.beginPath();
        const rad = isHov ? 5.5 : (isMod ? 4.2 : 2.2);
        ctx.arc(v.sx, v.sy, rad, 0, Math.PI * 2);
        if (isHov) {
            ctx.fillStyle = '#ffffff';
            ctx.strokeStyle = '#38bdf8';
            ctx.lineWidth = 2.2;
            ctx.fill();
            ctx.stroke();
        } else if (isMod) {
            ctx.fillStyle = '#fbbf24';
            ctx.strokeStyle = '#0f172a';
            ctx.lineWidth = 1.4;
            ctx.fill();
            ctx.stroke();
        } else {
            ctx.fillStyle = 'rgba(226, 232, 240, 0.65)';
            ctx.fill();
        }
    }

    ctx.restore();
}

let activeEditCell = null;

function initCellEditModal() {
    const modal = document.getElementById('modal-cell-edit');
    const physInput = document.getElementById('cell-input-phys');
    const rawInput = document.getElementById('cell-input-raw');

    physInput.addEventListener('input', () => {
        if (!currentMapObj) return;
        const p = parseFloat(physInput.value);
        if (!isNaN(p)) {
            const factor = currentMapObj.factor || 1.0;
            const offset = currentMapObj.offset || 0.0;
            rawInput.value = Math.round((p - offset) / factor);
        }
    });

    rawInput.addEventListener('input', () => {
        if (!currentMapObj) return;
        const r = parseInt(rawInput.value, 10);
        if (!isNaN(r)) {
            const factor = currentMapObj.factor || 1.0;
            const offset = currentMapObj.offset || 0.0;
            physInput.value = Math.round((r * factor + offset) * 1000) / 1000;
        }
    });

    document.getElementById('btn-cell-cancel').addEventListener('click', () => {
        modal.classList.remove('active');
    });

    document.getElementById('btn-cell-apply').addEventListener('click', () => {
        if (!activeEditCell) return;
        const raw = parseInt(rawInput.value, 10);
        const phys = parseFloat(physInput.value);

        if (isNaN(raw) || isNaN(phys)) {
            alert('Invalid value entered.');
            return;
        }

        // Add or update pending edits
        pendingEdits = pendingEdits.filter(e => !(e.row === activeEditCell.row && e.col === activeEditCell.col));
        pendingEdits.push({ row: activeEditCell.row, col: activeEditCell.col, raw: raw, physical: phys });

        // Update local grid view immediately
        if (currentMapData && currentMapData.cells[activeEditCell.row]) {
            currentMapData.cells[activeEditCell.row][activeEditCell.col].raw = raw;
            currentMapData.cells[activeEditCell.row][activeEditCell.col].physical = phys;
            currentMapData.cells[activeEditCell.row][activeEditCell.col].is_modified = true;
            renderMapGrid(currentMapData);
        }

        modal.classList.remove('active');
    });
}

function openCellEditModal(row, col) {
    if (!currentMapData || !currentMapData.cells[row] || !currentMapData.cells[row][col]) return;
    activeEditCell = { row, col };

    const cell = currentMapData.cells[row][col];
    const modal = document.getElementById('modal-cell-edit');

    document.getElementById('cell-coord-label').innerText = `Row ${row}, Col ${col} (Offset 0x${cell.file_offset.toString(16).toUpperCase()})`;
    document.getElementById('cell-input-phys').value = cell.physical;
    document.getElementById('cell-input-raw').value = cell.raw;
    document.getElementById('cell-edit-meta').innerText = `Scaling: Factor ${currentMapObj.factor}, Offset ${currentMapObj.offset} [${currentMapObj.unit || '-'}]`;

    modal.classList.add('active');
    document.getElementById('cell-input-phys').focus();
}

/* =========================================================================
   4. DFLASH / FEE BLOCK INSPECTOR
   ========================================================================= */
let activeDFlashCat = 'ALL';

function initDFlash() {
    document.querySelectorAll('#dflash-category-filters .cat-pill').forEach(pill => {
        pill.addEventListener('click', () => {
            document.querySelectorAll('#dflash-category-filters .cat-pill').forEach(p => p.classList.remove('active'));
            pill.classList.add('active');
            activeDFlashCat = pill.getAttribute('data-cat');
            loadDFlash();
        });
    });
}

async function loadDFlash() {
    try {
        const [secRes, blkRes] = await Promise.all([
            fetch('/api/dflash/sectors').then(r => r.json()),
            fetch(`/api/dflash/blocks?category=${encodeURIComponent(activeDFlashCat)}`).then(r => r.json())
        ]);

        // Sector cards
        const s1 = secRes.find(s => s.sector_index === 1);
        const s0 = secRes.find(s => s.sector_index === 0);
        if (s1) document.getElementById('sector-1-stats').innerText = `${s1.active_blocks} Active Blocks (${s1.block_count} total instances)`;
        if (s0) document.getElementById('sector-0-stats').innerText = `${s0.active_blocks} Shadow Blocks (${s0.block_count} total instances)`;

        // Table
        const tbody = document.querySelector('#table-dflash-blocks tbody');
        if (!blkRes || blkRes.length === 0) {
            tbody.innerHTML = '<tr><td colspan="11">No blocks found.</td></tr>';
            return;
        }

        tbody.innerHTML = blkRes.map(b => `
            <tr>
                <td class="mono"><strong>${b.block_id_hex}</strong></td>
                <td><strong>${escapeHtml(b.name)}</strong><br><small style="color:var(--text-muted);">${escapeHtml(b.description)}</small></td>
                <td><span class="badge ${b.category === 'SECURITY' ? 'danger' : b.category === 'IDENTITY' ? 'info' : 'warn'}">${escapeHtml(b.category)}</span></td>
                <td class="mono">Sec ${b.sector_idx}</td>
                <td class="mono">${b.offset_hex}</td>
                <td class="mono">${b.length}B</td>
                <td class="mono">v${b.version_count}</td>
                <td class="mono">${b.crc_hex}</td>
                <td><span class="badge ${b.is_active ? 'success' : 'warn'}">${b.is_active ? 'ACTIVE' : 'SHADOW'}</span></td>
                <td class="mono" style="max-width:200px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap;">${escapeHtml(b.payload_ascii || b.payload_hex)}</td>
                <td><button class="btn btn-secondary btn-sm" onclick="inspectDFlashBlock(${b.block_id})">Inspect</button></td>
            </tr>
        `).join('');
    } catch (err) {
        console.error('Failed to load DFLASH data:', err);
    }
}

async function inspectDFlashBlock(blockID) {
    const modal = document.getElementById('modal-dflash-block');
    const title = document.getElementById('modal-dflash-title');
    const meta = document.getElementById('modal-dflash-meta');
    const hexEl = document.getElementById('modal-dflash-hex');
    const timelineEl = document.getElementById('modal-dflash-timeline');

    modal.classList.add('active');
    hexEl.innerText = 'Loading payload...';
    timelineEl.innerHTML = '';

    try {
        const res = await fetch(`/api/dflash/blocks/${blockID}`).then(r => r.json());
        const act = res.active_block;

        title.innerText = `Block 0x${blockID.toString(16).toUpperCase().padStart(2, '0')}: ${act.name}`;
        meta.innerHTML = `
            <div class="editor-meta">
                <span class="meta-tag">Category: ${escapeHtml(act.category)}</span>
                <span class="meta-tag">Length: ${act.length} Bytes</span>
                <span class="meta-tag">CRC16: ${act.crc_hex}</span>
                <span class="meta-tag">Location: Offset ${act.offset_hex} (EA ${act.dflash_ea_hex})</span>
            </div>
            <p style="margin-top:6px; color:var(--text-muted);">${escapeHtml(act.description)}</p>
        `;

        // Render Hex Viewer
        hexEl.innerText = formatHexDump(act.payload_hex);

        // Render Revisions Timeline
        timelineEl.innerHTML = res.revisions.map((rev, idx) => `
            <div class="timeline-item ${rev.is_active && rev.sector_idx === 1 ? 'active' : ''}">
                <div class="timeline-title">
                    Generation ${res.revisions.length - idx}: Sector ${rev.sector_idx} (v${rev.version_count}) - ${rev.is_active ? 'ACTIVE' : 'SHADOW'}
                </div>
                <div class="timeline-desc">
                    Offset ${rev.offset_hex} | Length ${rev.length}B | CRC ${rev.crc_hex} | Preview: ${escapeHtml(rev.payload_ascii.trim() || rev.payload_hex.substring(0, 16))}
                </div>
            </div>
        `).join('');
    } catch (err) {
        hexEl.innerText = `Error loading block: ${err.message}`;
    }
}

function formatHexDump(hexStr) {
    if (!hexStr) return 'No payload data';
    const bytes = [];
    for (let i = 0; i < hexStr.length; i += 2) {
        bytes.push(parseInt(hexStr.substr(i, 2), 16));
    }

    let out = '';
    for (let i = 0; i < bytes.length; i += 16) {
        const chunk = bytes.slice(i, i + 16);
        const addr = '+0x' + i.toString(16).toUpperCase().padStart(4, '0') + ':  ';
        let hexPart = chunk.map(b => b.toString(16).toUpperCase().padStart(2, '0')).join(' ');
        hexPart = hexPart.padEnd(48, ' ');
        const asciiPart = chunk.map(b => (b >= 32 && b <= 126 ? String.fromCharCode(b) : '.')).join('');
        out += `${addr}${hexPart}  |${asciiPart}|\n`;
    }
    return out;
}

/* =========================================================================
   5. RAM SYMBOL DICTIONARY
   ========================================================================= */
let curatedRAMList = [];

function initRAM() {
    const filterInput = document.getElementById('ram-curated-search');
    filterInput.addEventListener('input', () => {
        const q = filterInput.value.toLowerCase();
        renderCuratedRAM(curatedRAMList.filter(s =>
            s.name.toLowerCase().includes(q) ||
            s.ea_hex.toLowerCase().includes(q) ||
            s.description.toLowerCase().includes(q)
        ));
    });

    document.getElementById('btn-inspect-ram').addEventListener('click', () => {
        const val = document.getElementById('ram-inspect-input').value.trim();
        if (val) inspectRAMAddress(val);
    });

    loadCuratedRAM();
}

async function loadCuratedRAM() {
    try {
        const res = await fetch('/api/ram/curated').then(r => r.json());
        curatedRAMList = res;
        renderCuratedRAM(res);
    } catch (err) {
        console.error('Failed to load RAM symbols:', err);
    }
}

function renderCuratedRAM(symbols) {
    const tbody = document.querySelector('#table-ram-curated tbody');
    if (!symbols || symbols.length === 0) {
        tbody.innerHTML = '<tr><td colspan="10">No variables match filter.</td></tr>';
        return;
    }

    tbody.innerHTML = symbols.map(s => `
        <tr>
            <td class="mono"><strong><a href="#" onclick="inspectRAMAddress('${s.ea_hex}'); return false;">${s.ea_hex}</a></strong></td>
            <td><strong>${escapeHtml(s.name)}</strong></td>
            <td><span class="badge info">${escapeHtml(s.partition)}</span></td>
            <td>${escapeHtml(s.data_type)} <small>(${escapeHtml(s.unit)})</small></td>
            <td class="mono" style="font-size:11px;">${escapeHtml(s.scaling)}</td>
            <td style="color:var(--text-muted); font-size:11px;">${escapeHtml(s.description)}</td>
            <td class="mono">${s.reads}</td>
            <td class="mono">${s.writes}</td>
            <td>
                ${(s.producers || []).slice(0, 3).map(p => `
                    <a href="#" class="ref-link" onclick="jumpToFunction('${p.function_ea_hex}'); return false;" title="Insn ${p.insn_ea_hex}: ${p.mnemonic} ${p.op_str}">
                        ${escapeHtml(p.function_name || p.function_ea_hex)}
                    </a>
                `).join(' ')}
                ${(s.producers || []).length > 3 ? `<small>+${s.producers.length - 3}</small>` : ''}
            </td>
            <td>
                ${(s.consumers || []).slice(0, 3).map(c => `
                    <a href="#" class="ref-link" onclick="jumpToFunction('${c.function_ea_hex}'); return false;" title="Insn ${c.insn_ea_hex}: ${c.mnemonic} ${c.op_str}">
                        ${escapeHtml(c.function_name || c.function_ea_hex)}
                    </a>
                `).join(' ')}
                ${(s.consumers || []).length > 3 ? `<small>+${s.consumers.length - 3}</small>` : ''}
            </td>
        </tr>
    `).join('');
}

async function inspectRAMAddress(eaStr) {
    const resultEl = document.getElementById('ram-inspect-result');
    resultEl.style.display = 'block';
    resultEl.innerHTML = '<div class="loading">Inspecting address references...</div>';

    try {
        const res = await fetch(`/api/ram/inspect/${encodeURIComponent(eaStr)}`).then(r => r.json());
        resultEl.innerHTML = `
            <div class="editor-meta mb-3">
                <span class="meta-tag">Address: ${res.ea_hex}</span>
                <span class="meta-tag">Total Accesses: ${res.total}</span>
                <span class="meta-tag">Reads: ${res.reads}</span>
                <span class="meta-tag">Writes: ${res.writes}</span>
            </div>
            <div style="display:grid; grid-template-columns: 1fr 1fr; gap:16px;">
                <div>
                    <h4>Producers (Writing Functions: ${res.producers ? res.producers.length : 0})</h4>
                    <table class="data-table">
                        <thead><tr><th>Function</th><th>Insn EA</th><th>Mnemonic</th></tr></thead>
                        <tbody>
                            ${(res.producers || []).map(p => `
                                <tr>
                                    <td><a href="#" class="ref-link" onclick="jumpToFunction('${p.function_ea_hex}'); return false;">${escapeHtml(p.function_name || p.function_ea_hex)}</a></td>
                                    <td class="mono">${p.insn_ea_hex}</td>
                                    <td class="mono">${escapeHtml(p.mnemonic)} ${escapeHtml(p.op_str)}</td>
                                </tr>
                            `).join('') || '<tr><td colspan="3">None</td></tr>'}
                        </tbody>
                    </table>
                </div>
                <div>
                    <h4>Consumers (Reading Functions: ${res.consumers ? res.consumers.length : 0})</h4>
                    <table class="data-table">
                        <thead><tr><th>Function</th><th>Insn EA</th><th>Mnemonic</th></tr></thead>
                        <tbody>
                            ${(res.consumers || []).map(c => `
                                <tr>
                                    <td><a href="#" class="ref-link" onclick="jumpToFunction('${c.function_ea_hex}'); return false;">${escapeHtml(c.function_name || c.function_ea_hex)}</a></td>
                                    <td class="mono">${c.insn_ea_hex}</td>
                                    <td class="mono">${escapeHtml(c.mnemonic)} ${escapeHtml(c.op_str)}</td>
                                </tr>
                            `).join('') || '<tr><td colspan="3">None</td></tr>'}
                        </tbody>
                    </table>
                </div>
            </div>
        `;
    } catch (err) {
        resultEl.innerHTML = `<div style="color:var(--red);">Error inspecting address: ${escapeHtml(err.message)}</div>`;
    }
}

/* =========================================================================
   6. UDS DIAGNOSTIC DIDS
   ========================================================================= */
let allDIDsCache = [];

function initDIDs() {
    const searchInput = document.getElementById('did-search');
    if (searchInput) {
        searchInput.addEventListener('input', () => {
            renderDIDsTable(allDIDsCache);
        });
    }
    loadDIDs();
}

async function loadDIDs() {
    try {
        allDIDsCache = await fetch('/api/dids').then(r => r.json());
        renderDIDsTable(allDIDsCache);
    } catch (err) {
        console.error('Failed to load DIDs:', err);
    }
}

function renderDIDsTable(dids) {
    const tbody = document.querySelector('#table-dids tbody');
    if (!tbody) return;

    const searchInput = document.getElementById('did-search');
    const filter = searchInput ? searchInput.value.trim().toLowerCase() : '';

    const filtered = (dids || []).filter(d => {
        if (!filter) return true;
        return (d.did && d.did.toLowerCase().includes(filter)) ||
               (d.name && d.name.toLowerCase().includes(filter)) ||
               (d.description && d.description.toLowerCase().includes(filter)) ||
               (d.handler_name && d.handler_name.toLowerCase().includes(filter)) ||
               (d.handler_ea && d.handler_ea.toLowerCase().includes(filter));
    });

    if (filtered.length === 0) {
        tbody.innerHTML = '<tr><td colspan="9" class="empty-state">No matching UDS DIDs found.</td></tr>';
        return;
    }

    tbody.innerHTML = filtered.map(d => {
        const handlerHtml = renderHandlerLinks(d.handler_ea, d.handler_name);
        return `
            <tr>
                <td class="mono"><strong>${escapeHtml(d.did)}</strong></td>
                <td><strong>${escapeHtml(d.name)}</strong></td>
                <td><span class="badge ${d.access.includes('Write') ? 'warn' : 'info'}">${escapeHtml(d.access)}</span></td>
                <td>${escapeHtml(d.sessions)}</td>
                <td><span class="badge ${d.security.includes('None') ? 'success' : 'danger'}">${escapeHtml(d.security)}</span></td>
                <td class="mono">${escapeHtml(d.memory_target)}</td>
                <td class="mono">${d.length_bytes}B</td>
                <td>${handlerHtml}</td>
                <td style="color:var(--text-muted); font-size:11px;">${escapeHtml(d.description)}</td>
            </tr>
        `;
    }).join('');
}

function renderHandlerLinks(eaStr, nameStr) {
    if (!eaStr) return escapeHtml(nameStr || '-');
    const matches = eaStr.match(/0x[0-9A-Fa-f]+/g);
    if (!matches || matches.length === 0) {
        return escapeHtml(nameStr || eaStr);
    }
    return matches.map(ea => {
        let label = ea;
        if (eaStr.includes(`${ea} (Read)`)) label = `${ea} (Read)`;
        else if (eaStr.includes(`${ea} (Write)`)) label = `${ea} (Write)`;
        else if (eaStr.includes(`${ea} (SSW)`)) label = `${ea} (SSW)`;
        else if (eaStr.includes(`${ea} (App)`)) label = `${ea} (App)`;
        return `<a href="#" class="ref-link" style="margin-right: 4px; display:inline-block; margin-bottom:2px;" onclick="jumpToFunction('${ea}'); return false;">${escapeHtml(label)}</a>`;
    }).join(' ');
}

/* =========================================================================
   7. FUNCTION & DISASSEMBLY BROWSER
   ========================================================================= */
let currentFnEA = null;

function initDisasm() {
    const searchInput = document.getElementById('fn-search');
    const partFilter = document.getElementById('fn-partition-filter');
    const clearBtn = document.getElementById('btn-clear-fn-search');

    let timer = null;
    searchInput.addEventListener('input', () => {
        clearTimeout(timer);
        timer = setTimeout(() => searchFunctions(), 250);
    });

    partFilter.addEventListener('change', () => searchFunctions());
    clearBtn.addEventListener('click', () => {
        searchInput.value = '';
        searchFunctions();
    });

    // Quick jump chips
    document.querySelectorAll('.chip').forEach(chip => {
        chip.addEventListener('click', () => {
            const ea = chip.getAttribute('data-fn');
            jumpToFunction(ea);
        });
    });

    // Subtabs in Function view
    document.querySelectorAll('.sub-tab').forEach(tab => {
        tab.addEventListener('click', () => {
            document.querySelectorAll('.sub-tab').forEach(t => t.classList.remove('active'));
            document.querySelectorAll('.sub-content').forEach(c => c.classList.remove('active'));
            tab.classList.add('active');
            const sub = tab.getAttribute('data-sub');
            const target = document.getElementById(`sub-${sub}`);
            if (target) target.classList.add('active');
        });
    });

    searchFunctions();
}

async function searchFunctions() {
    const q = document.getElementById('fn-search').value.trim();
    const part = document.getElementById('fn-partition-filter').value;
    const listEl = document.getElementById('fn-list');
    const badge = document.getElementById('fn-count-badge');

    listEl.innerHTML = '<div class="loading">Searching functions...</div>';

    try {
        const res = await fetch(`/api/functions?q=${encodeURIComponent(q)}&partition=${encodeURIComponent(part)}&limit=100`).then(r => r.json());
        badge.innerText = `${res.total.toLocaleString()} functions`;

        if (!res.functions || res.functions.length === 0) {
            listEl.innerHTML = '<div class="empty-state"><p>No functions found.</p></div>';
            return;
        }

        listEl.innerHTML = res.functions.map(f => `
            <div class="list-item ${currentFnEA === f.ea ? 'selected' : ''}" data-ea="${f.ea_hex}">
                <div class="list-item-title">
                    <span>${escapeHtml(f.name)}</span>
                    <span class="badge info">${escapeHtml(f.partition_name)}</span>
                </div>
                <div class="list-item-meta">
                    ${f.ea_hex} .. ${f.end_ea_hex} | ${f.insn_count} insns | ${f.size_bytes}B
                </div>
            </div>
        `).join('');

        listEl.querySelectorAll('.list-item').forEach(item => {
            item.addEventListener('click', () => {
                const ea = item.getAttribute('data-ea');
                loadFunctionDetail(ea);
            });
        });
    } catch (err) {
        listEl.innerHTML = `<div class="empty-state" style="color:var(--red);">Error: ${escapeHtml(err.message)}</div>`;
    }
}

async function loadFunctionDetail(eaHex) {
    const placeholder = document.getElementById('fn-editor-placeholder');
    const content = document.getElementById('fn-detail-content');

    placeholder.style.display = 'none';
    content.style.display = 'flex';

    try {
        const res = await fetch(`/api/functions/${eaHex}`).then(r => r.json());
        const f = res ? res.function : null;
        if (!f) {
            placeholder.style.display = 'flex';
            placeholder.innerHTML = `
                <div class="empty-state" style="max-width: 480px; text-align: center; margin: auto;">
                    <div style="font-size: 3rem; margin-bottom: 1rem;">🔍</div>
                    <h3 style="color: var(--text-bright); margin-bottom: 0.5rem;">Function Not Registered</h3>
                    <p style="color: var(--text-dim); font-size: 0.9rem; line-height: 1.5;">
                        Address <code style="color: var(--accent); background: var(--bg-card); padding: 2px 6px; border-radius: 4px;">${escapeHtml(eaHex)}</code> was not found in the function index of the database.
                    </p>
                    <p style="color: var(--text-dim); font-size: 0.85rem; margin-top: 0.75rem;">
                        This address may be an unanalyzed sub-routine, raw data/table, or outside the mapped firmware partitions.
                    </p>
                </div>
            `;
            content.style.display = 'none';
            return;
        }
        currentFnEA = f.ea;

        // Headers
        document.getElementById('cur-fn-name').innerText = f.name;
        document.getElementById('cur-fn-range').innerText = `Range: ${f.ea_hex} .. ${f.end_ea_hex}`;
        document.getElementById('cur-fn-part').innerText = `Partition: ${f.partition_name}`;
        document.getElementById('cur-fn-size').innerText = `Size: ${f.size_bytes} Bytes`;
        document.getElementById('cur-fn-insns').innerText = `${f.insn_count} Instructions`;
        document.getElementById('cur-fn-ret').innerText = f.has_return ? 'Returns: YES' : 'NoReturn (Trap/Hang)';

        // Subtab counters
        document.getElementById('fn-sub-insn-count').innerText = res.instructions ? res.instructions.length : 0;
        document.getElementById('fn-sub-caller-count').innerText = res.callers ? res.callers.length : 0;
        document.getElementById('fn-sub-callee-count').innerText = res.callees ? res.callees.length : 0;
        document.getElementById('fn-sub-mem-count').innerText = res.memory_access ? res.memory_access.length : 0;

        // Disassembly Listing
        const disasmEl = document.getElementById('disasm-code');
        disasmEl.innerHTML = (res.instructions || []).map(i => {
            const isCall = i.is_call;
            return `
                <div class="disasm-line">
                    <span class="disasm-ea">${i.ea_hex}</span>
                    <span class="disasm-mnemonic ${isCall ? 'disasm-call' : ''}">${escapeHtml(i.mnemonic)}</span>
                    <span class="disasm-op">${escapeHtml(i.op_str)}</span>
                </div>
            `;
        }).join('');

        // Callers Table
        const callersTbody = document.querySelector('#table-fn-callers tbody');
        callersTbody.innerHTML = (res.callers || []).map(c => `
            <tr>
                <td class="mono">${c.caller_ea_hex}</td>
                <td><a href="#" class="ref-link" onclick="jumpToFunction('${c.caller_fn_hex}'); return false;">${escapeHtml(c.caller_fn_name || c.caller_fn_hex)}</a></td>
                <td><span class="badge info">${escapeHtml(c.caller_partition)}</span></td>
                <td>${escapeHtml(c.call_type)}</td>
            </tr>
        `).join('') || '<tr><td colspan="4">No callers recorded.</td></tr>';

        // Callees Table
        const calleesTbody = document.querySelector('#table-fn-callees tbody');
        calleesTbody.innerHTML = (res.callees || []).map(c => `
            <tr>
                <td class="mono">${c.caller_ea_hex}</td>
                <td class="mono">${c.callee_ea_hex}</td>
                <td><a href="#" class="ref-link" onclick="jumpToFunction('${c.callee_fn_hex}'); return false;">${escapeHtml(c.callee_fn_name || c.callee_fn_hex)}</a></td>
                <td><span class="badge info">${escapeHtml(c.callee_partition)}</span></td>
                <td>${escapeHtml(c.call_type)}</td>
            </tr>
        `).join('') || '<tr><td colspan="5">No callees recorded.</td></tr>';

        // Memory Access Table
        const memTbody = document.querySelector('#table-fn-memory tbody');
        memTbody.innerHTML = (res.memory_access || []).map(m => `
            <tr>
                <td class="mono">${m.insn_ea_hex}</td>
                <td class="mono"><a href="#" onclick="inspectRAMAddress('${m.target_ea_hex}'); return false;">${m.target_ea_hex}</a></td>
                <td><span class="badge info">${escapeHtml(m.target_partition)}</span></td>
                <td><span class="badge ${m.access_type === 'WRITE' ? 'warn' : 'info'}">${escapeHtml(m.access_type)}</span></td>
                <td class="mono">${m.access_width}B</td>
                <td>${escapeHtml(m.target_symbol || '-')}</td>
                <td class="mono">${escapeHtml(m.mnemonic)} ${escapeHtml(m.op_str)}</td>
            </tr>
        `).join('') || '<tr><td colspan="7">No memory interactions recorded.</td></tr>';

    } catch (err) {
        console.error('Failed to load function detail:', err);
        placeholder.style.display = 'flex';
        placeholder.innerHTML = `
            <div class="empty-state" style="max-width: 480px; text-align: center; margin: auto; color: var(--red);">
                <div style="font-size: 3rem; margin-bottom: 1rem;">⚠️</div>
                <h3 style="margin-bottom: 0.5rem;">Error Loading Disassembly</h3>
                <p style="font-size: 0.9rem;">${escapeHtml(err.message)}</p>
            </div>
        `;
        content.style.display = 'none';
    }
}

function jumpToFunction(eaHex) {
    switchToTab('disasm');
    const input = document.getElementById('fn-search');
    input.value = eaHex;
    loadFunctionDetail(eaHex);
}

/* =========================================================================
   8. DIFFS & BIN EXPORT
   ========================================================================= */
function initDiffsAndExport() {
    const btnDiffs = document.getElementById('btn-view-diffs');
    const btnExport = document.getElementById('btn-export-bin');
    const modalDiffs = document.getElementById('modal-diffs');

    btnDiffs.addEventListener('click', async () => {
        await loadDiffsModal();
        modalDiffs.classList.add('active');
    });

    btnExport.addEventListener('click', () => {
        exportBinary();
    });

    document.getElementById('btn-modal-export').addEventListener('click', () => {
        modalDiffs.classList.remove('active');
        exportBinary();
    });

    document.getElementById('btn-modal-reset-diffs').addEventListener('click', async () => {
        if (confirm('Discard all uncommitted session edits?')) {
            await fetch('/api/diffs/reset', { method: 'POST' });
            updateDiffCount();
            modalDiffs.classList.remove('active');
            if (currentMapObj) loadMapDetail(currentMapObj.id);
        }
    });

    updateDiffCount();
}

async function updateDiffCount() {
    try {
        const diffs = await fetch('/api/diffs').then(r => r.json());
        const count = diffs ? diffs.length : 0;
        document.getElementById('diff-counter').innerText = count;
    } catch (err) {
        console.error('Error fetching diffs:', err);
    }
}

async function loadDiffsModal() {
    const tbody = document.querySelector('#table-diffs tbody');
    try {
        const diffs = await fetch('/api/diffs').then(r => r.json());
        if (!diffs || diffs.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6">No session diffs recorded.</td></tr>';
            return;
        }

        tbody.innerHTML = diffs.map(d => `
            <tr>
                <td><strong>${escapeHtml(d.map_name)}</strong></td>
                <td class="mono">0x${d.file_offset.toString(16).toUpperCase()}</td>
                <td class="mono">[${d.row}, ${d.col}]</td>
                <td class="mono">${d.old_physical} <small>(${d.old_raw})</small></td>
                <td class="mono" style="color:var(--amber);"><strong>${d.new_physical} <small>(${d.new_raw})</small></strong></td>
                <td>${escapeHtml(d.unit || '-')}</td>
            </tr>
        `).join('');
    } catch (err) {
        tbody.innerHTML = `<tr><td colspan="6">Error loading diffs: ${err.message}</td></tr>`;
    }
}

async function exportBinary() {
    const outputPath = prompt('Enter destination output path for exported binary:', 'Claude_Bins/web_editor_export.bin');
    if (!outputPath) return;

    try {
        const res = await fetch('/api/bin/export', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ output_path: outputPath })
        }).then(r => r.json());

        if (res.success) {
            alert(`Binary export SUCCESS!\n\nDestination: ${res.output_path}\nCalculated Launch-Gate CRC: ${res.crc_hex}\n${res.launch_gate}`);
        } else {
            alert(`Export failed: ${res.error}`);
        }
    } catch (err) {
        alert(`Export request error: ${err.message}`);
    }
}

/* =========================================================================
   8. ACTIVE BINARY SELECTOR & UPLOAD
   ========================================================================= */

let availableBinsCache = [];

function initBinSelector() {
    const btnOpen = document.getElementById('btn-open-bin-modal');
    const modal = document.getElementById('modal-bin-selector');
    const btnClose = document.getElementById('modal-bin-close');
    const filterInput = document.getElementById('bin-filter-input');
    const dropzone = document.getElementById('bin-dropzone');
    const fileInput = document.getElementById('bin-file-input');
    const btnBrowse = document.getElementById('btn-browse-bin');

    if (btnOpen && modal) {
        btnOpen.addEventListener('click', () => {
            modal.classList.add('active');
            if (filterInput) filterInput.value = '';
            loadAvailableBins();
        });
    }

    if (btnClose && modal) {
        btnClose.addEventListener('click', () => {
            modal.classList.remove('active');
        });
    }

    // Close when clicking modal backdrop
    if (modal) {
        modal.addEventListener('click', (e) => {
            if (e.target === modal) {
                modal.classList.remove('active');
            }
        });
    }

    // Filter input listener
    if (filterInput) {
        filterInput.addEventListener('input', () => {
            renderBinsList(availableBinsCache);
        });
    }

    // File browse click
    if (btnBrowse && fileInput) {
        btnBrowse.addEventListener('click', (e) => {
            e.preventDefault();
            fileInput.click();
        });
    }

    // Dropzone drag-and-drop
    if (dropzone && fileInput) {
        ['dragenter', 'dragover'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                e.stopPropagation();
                dropzone.classList.add('dragover');
            });
        });

        ['dragleave', 'drop'].forEach(eventName => {
            dropzone.addEventListener(eventName, (e) => {
                e.preventDefault();
                e.stopPropagation();
                dropzone.classList.remove('dragover');
            });
        });

        dropzone.addEventListener('drop', (e) => {
            const dt = e.dataTransfer;
            const files = dt.files;
            if (files && files.length > 0) {
                uploadBinaryFile(files[0]);
            }
        });

        dropzone.addEventListener('click', (e) => {
            if (e.target !== btnBrowse) {
                fileInput.click();
            }
        });

        fileInput.addEventListener('change', (e) => {
            if (e.target.files && e.target.files.length > 0) {
                uploadBinaryFile(e.target.files[0]);
            }
        });
    }

    // Initial fetch of active bin name for header
    loadActiveBinHeader();
}

async function loadActiveBinHeader() {
    try {
        const res = await fetch('/api/bin/list').then(r => r.json());
        if (res && res.active_name) {
            const hdrName = document.getElementById('hdr-bin-name');
            if (hdrName) {
                hdrName.innerText = res.active_name;
                hdrName.title = res.active_path || res.active_name;
            }
        }
    } catch (err) {
        console.error('Failed to load active bin header:', err);
    }
}

let availableDBsCache = [];
let activeDBPathCache = '';

async function refreshAllTabsAfterSwitch() {
    currentMapObj = null;
    currentMapData = null;
    currentFnEA = null;

    const mapContent = document.getElementById('map-editor-content');
    const mapPlaceholder = document.getElementById('map-editor-placeholder');
    if (mapContent) mapContent.style.display = 'none';
    if (mapPlaceholder) mapPlaceholder.style.display = 'flex';

    const fnContent = document.getElementById('fn-detail-content');
    const fnPlaceholder = document.getElementById('fn-editor-placeholder');
    if (fnContent) fnContent.style.display = 'none';
    if (fnPlaceholder) fnPlaceholder.style.display = 'flex';

    // Update partition filter dropdown dynamically for the active ECU database
    try {
        const parts = await fetch('/api/partitions').then(r => r.json());
        const partSelect = document.getElementById('fn-partition-filter');
        if (partSelect && Array.isArray(parts)) {
            partSelect.innerHTML = `<option value="ALL">All Partitions</option>` +
                parts.map(p => `<option value="${escapeHtml(p.name)}">${escapeHtml(p.name)} (${escapeHtml(p.memory_type || '')})</option>`).join('');
            partSelect.value = 'ALL';
        }
    } catch (e) {
        console.error('Failed to refresh partitions dropdown:', e);
    }

    if (typeof loadDashboard === 'function') loadDashboard();
    if (typeof searchMaps === 'function') searchMaps();
    if (typeof loadDFlash === 'function') loadDFlash();
    if (typeof loadCuratedRAM === 'function') loadCuratedRAM();
    if (typeof loadDIDs === 'function') loadDIDs();
    if (typeof searchFunctions === 'function') searchFunctions();
}

async function loadAvailableBins() {
    const listEl = document.getElementById('available-bins-list');
    if (!listEl) return;
    listEl.innerHTML = '<div class="loading">Scanning available ECU databases & binaries...</div>';

    try {
        const res = await fetch('/api/bin/list').then(r => r.json());
        if (res && res.active_name) {
            const hdrName = document.getElementById('hdr-bin-name');
            if (hdrName) {
                hdrName.innerText = res.active_name;
                hdrName.title = `${res.active_path || res.active_name} (DB: ${res.active_db || 'flash_analysis.db'})`;
            }
        }

        availableBinsCache = res.bins || [];
        availableDBsCache = res.dbs || [];
        activeDBPathCache = res.active_db || '';
        renderBinsList(availableBinsCache);
    } catch (err) {
        listEl.innerHTML = `<div class="empty-state"><p>Error loading binaries: ${escapeHtml(err.message)}</p></div>`;
    }
}

function renderBinsList(bins) {
    const listEl = document.getElementById('available-bins-list');
    const filterInput = document.getElementById('bin-filter-input');
    if (!listEl) return;

    const filter = filterInput ? filterInput.value.trim().toLowerCase() : '';
    const filtered = bins.filter(b => {
        if (!filter) return true;
        return (b.filename && b.filename.toLowerCase().includes(filter)) ||
               (b.folder && b.folder.toLowerCase().includes(filter)) ||
               (b.category && b.category.toLowerCase().includes(filter));
    });

    let dbSectionHtml = '';
    if (availableDBsCache && availableDBsCache.length > 0 && !filter) {
        dbSectionHtml = `<div style="margin-bottom: 10px; font-size: 11px; font-weight: 700; color: var(--accent-cyan); text-transform: uppercase; letter-spacing: 0.06em;">ECU Analysis Databases (.db)</div>` +
            availableDBsCache.map(db => {
                const isActive = activeDBPathCache && (activeDBPathCache === db.path || activeDBPathCache.endsWith(db.filename));
                const activeClass = isActive ? 'active-bin' : '';
                const badgeActive = isActive ? '<span class="badge badge-success" style="font-size:10px;">ACTIVE DB</span>' : '';
                const actionBtn = isActive
                    ? `<span class="badge badge-success" style="padding: 6px 12px; font-weight:700;">ACTIVE</span>`
                    : `<button class="btn btn-sm btn-primary select-db-btn" data-path="${escapeHtml(db.path)}" data-filename="${escapeHtml(db.filename)}">Switch ECU DB</button>`;
                return `
                    <div class="bin-card ${activeClass}" style="border-left: 3px solid var(--accent-cyan); margin-bottom: 6px;">
                        <div class="bin-card-left">
                            <div style="display:flex; align-items:center; gap:8px;">
                                <span class="bin-card-title">${escapeHtml(db.filename)}</span>
                                ${badgeActive}
                                <span class="badge info" style="font-size:10px;">SQLite DB</span>
                            </div>
                            <div style="font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); margin-top: 2px;">
                                ${escapeHtml(db.category || '')} &bull; ${escapeHtml(String(db.size_mb || ''))}
                            </div>
                        </div>
                        <div class="bin-card-right">
                            ${actionBtn}
                        </div>
                    </div>
                `;
            }).join('') +
            `<div style="margin: 12px 0 8px 0; font-size: 11px; font-weight: 700; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.06em;">Calibration & Firmware Binaries (.bin)</div>`;
    }

    if (filtered.length === 0 && !dbSectionHtml) {
        listEl.innerHTML = '<div class="empty-state" style="padding: 20px;"><p>No matching binaries found.</p></div>';
        return;
    }

    listEl.innerHTML = dbSectionHtml + filtered.map(b => {
        const isActive = b.is_active;
        const activeClass = isActive ? 'active-bin' : '';
        const badgeActive = isActive ? '<span class="badge badge-success" style="font-size:10px;">ACTIVE</span>' : '';
        const categoryBadge = `<span class="badge info" style="font-size:10px;">${escapeHtml(b.category)}</span>`;

        const actionBtn = isActive
            ? `<span class="badge badge-success" style="padding: 6px 12px; font-weight:700;">LOADED</span>`
            : `<button class="btn btn-sm btn-outline select-bin-btn" data-path="${escapeHtml(b.path)}" data-filename="${escapeHtml(b.filename)}">Load</button>`;

        return `
            <div class="bin-card ${activeClass}">
                <div class="bin-card-left">
                    <div style="display:flex; align-items:center; gap:8px;">
                        <span class="bin-card-title">${escapeHtml(b.filename)}</span>
                        ${badgeActive}
                        ${categoryBadge}
                    </div>
                    <div style="font-size: 11px; color: var(--text-muted); font-family: var(--font-mono); margin-top: 2px;">
                        ${escapeHtml(b.folder)} &bull; ${escapeHtml(b.size_mb)} &bull; ${escapeHtml(b.mod_time)}
                    </div>
                </div>
                <div class="bin-card-right">
                    ${actionBtn}
                </div>
            </div>
        `;
    }).join('');

    // Attach click listeners to DB select buttons
    listEl.querySelectorAll('.select-db-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
            e.stopPropagation();
            const path = btn.getAttribute('data-path');
            const filename = btn.getAttribute('data-filename');
            selectDatabase(path, filename);
        });
    });

    // Attach click listeners to select buttons
    listEl.querySelectorAll('.select-bin-btn').forEach(btn => {
        btn.addEventListener('click', (e) => {
            e.stopPropagation();
            const path = btn.getAttribute('data-path');
            const filename = btn.getAttribute('data-filename');
            selectBinary(path, filename);
        });
    });
}

async function selectDatabase(path, filename) {
    try {
        const res = await fetch('/api/db/select', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path })
        }).then(r => r.json());

        if (res.success) {
            const hdrName = document.getElementById('hdr-bin-name');
            if (hdrName && res.filename) {
                hdrName.innerText = res.filename;
                hdrName.title = `${res.bin_path} (DB: ${res.db_path})`;
            }
            const modal = document.getElementById('modal-bin-selector');
            if (modal) modal.classList.remove('active');
            pendingEdits = [];
            updateDiffCount();
            await refreshAllTabsAfterSwitch();
            loadAvailableBins();
        } else {
            alert(`Failed to switch database: ${res.error || 'Unknown error'}`);
        }
    } catch (err) {
        alert(`Request failed: ${err.message}`);
    }
}

async function selectBinary(path, filename) {
    // Check for uncommitted diffs
    const diffCountEl = document.getElementById('diff-counter');
    const diffCount = diffCountEl ? parseInt(diffCountEl.innerText) || 0 : 0;
    if (diffCount > 0) {
        if (!confirm(`You have ${diffCount} uncommitted calibration edits in memory.\n\nSwitching to "${filename}" will clear pending edits. Proceed?`)) {
            return;
        }
    }

    try {
        const res = await fetch('/api/bin/select', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path })
        }).then(r => r.json());

        if (res.success) {
            const hdrName = document.getElementById('hdr-bin-name');
            if (hdrName) {
                hdrName.innerText = res.filename;
                hdrName.title = `${res.path} (DB: ${res.db_path || ''})`;
            }

            // Close modal
            const modal = document.getElementById('modal-bin-selector');
            if (modal) modal.classList.remove('active');

            // Reset pending client edits and update diff count
            pendingEdits = [];
            updateDiffCount();

            await refreshAllTabsAfterSwitch();

            // Refresh binary list cache
            loadAvailableBins();
        } else {
            alert(`Failed to load binary: ${res.error || 'Unknown error'}`);
        }
    } catch (err) {
        alert(`Request failed: ${err.message}`);
    }
}

async function uploadBinaryFile(file) {
    if (!file) return;

    if (!file.name.toLowerCase().endsWith('.bin')) {
        alert('Invalid file: only .bin calibration/firmware files are supported.');
        return;
    }

    const validSizes = [4194304, 786432, 785408, 49152, 32768, 16384];
    if (!validSizes.includes(file.size)) {
        if (!confirm(`Warning: Expected 4 MB (SIM2K-260), 768 KB / 767 KB (Kefico CPTSH TCU), or 48 KB / 32 KB / 16 KB (AWD ECU) binary.\nThe selected file is ${file.size.toLocaleString()} bytes.\n\nDo you still wish to attempt loading it?`)) {
            return;
        }
    }

    // Check uncommitted diffs
    const diffCountEl = document.getElementById('diff-counter');
    const diffCount = diffCountEl ? parseInt(diffCountEl.innerText) || 0 : 0;
    if (diffCount > 0) {
        if (!confirm(`You have ${diffCount} uncommitted calibration edits in memory.\n\nUploading and loading "${file.name}" will clear pending edits. Proceed?`)) {
            return;
        }
    }

    const formData = new FormData();
    formData.append('file', file);

    const listEl = document.getElementById('available-bins-list');
    if (listEl) {
        listEl.innerHTML = `<div class="loading">Uploading ${escapeHtml(file.name)} (${(file.size / 1024).toFixed(1)} KB)...</div>`;
    }

    try {
        const res = await fetch('/api/bin/upload', {
            method: 'POST',
            body: formData
        }).then(r => r.json());

        if (res.success) {
            const hdrName = document.getElementById('hdr-bin-name');
            if (hdrName) {
                hdrName.innerText = res.filename;
                hdrName.title = `${res.path} (DB: ${res.db_path || ''})`;
            }

            // Close modal
            const modal = document.getElementById('modal-bin-selector');
            if (modal) modal.classList.remove('active');

            // Reset pending edits
            pendingEdits = [];
            updateDiffCount();

            await refreshAllTabsAfterSwitch();

            alert(`Binary "${res.filename}" uploaded and loaded successfully!`);
            loadAvailableBins();
        } else {
            alert(`Upload failed: ${res.error || 'Server rejected file'}`);
            loadAvailableBins();
        }
    } catch (err) {
        alert(`Upload error: ${err.message}`);
        loadAvailableBins();
    }
}


