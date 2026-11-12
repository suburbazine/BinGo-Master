package main

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// buildSyntheticMDPSDB creates a realistic custom MDPS SQLite database exercising:
// - Hex string & integer EA columns
// - Alias columns (cols, data_type, units, description)
// - Nullable fields (folder=NULL, description=NULL, ref_functions=NULL)
// - Missing optional tables (no dflash_blocks, no calls, no memory_interactions)
// - Custom partitions (BOOT, CODE, CAL, SRAM)
// - 1D Axis maps referenced by a 2D Assist Current map
// - ARM Cortex-M 'bl' call instruction
func buildSyntheticMDPSDB(t *testing.T, dbFile string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open synthetic MDPS db: %v", err)
	}
	defer db.Close()

	ddl := `
	CREATE TABLE partitions (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		start_ea TEXT NOT NULL,
		end_ea TEXT NOT NULL,
		file_start INTEGER,
		file_end INTEGER,
		memory_type TEXT,
		description TEXT
	);
	INSERT INTO partitions (id, name, start_ea, end_ea, file_start, file_end, memory_type, description) VALUES
		(1, 'BOOT', '0x08000000', '0x08003FFF', 0, 16383, 'FLASH', 'MDPS Bootloader'),
		(2, 'CODE', '0x08004000', '0x0800FFFF', 16384, 65535, 'FLASH', 'MDPS Application Code'),
		(3, 'CAL',  '0x08010000', '0x0801FFFF', 65536, 131071, 'FLASH', 'MDPS Assist Calibration Area'),
		(4, 'SRAM', '0x20000000', '0x2000FFFF', NULL, NULL, 'RAM', 'Internal Cortex-M4 SRAM');

	CREATE TABLE cal_maps (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		file_offset INTEGER NOT NULL,
		ea TEXT NOT NULL,
		folder TEXT,
		map_type TEXT,
		cols INTEGER,
		rows INTEGER,
		data_type TEXT,
		units TEXT,
		factor REAL,
		offset REAL,
		axis_x_ea TEXT,
		axis_x_name TEXT,
		axis_y_ea TEXT,
		axis_y_name TEXT,
		description TEXT,
		ref_count INTEGER,
		ref_functions TEXT
	);

	-- Map 1: X Axis (Vehicle Speed kph, 4 points, uint16 LE, factor 0.1) at CAL+0x0100
	INSERT INTO cal_maps VALUES
		(1, 'Axis_VehicleSpeed_kph', 65792, '0x08010100', 'Axes', '1D', 4, 1, 'uint16', 'km/h', 0.1, 0.0, NULL, NULL, NULL, NULL, 'Vehicle speed breakpoints', 2, NULL);

	-- Map 2: Y Axis (Steering Torque Nm, 3 points, int16 LE, factor 0.01) at CAL+0x0120
	INSERT INTO cal_maps VALUES
		(2, 'Axis_SteerTorque_Nm', 65824, '0x08010120', NULL, '1D', 3, 1, 'int16', 'Nm', 0.01, 0.0, NULL, NULL, NULL, NULL, NULL, 2, NULL);

	-- Map 3: 2D Assist Current Map (cols=4, rows=3, uint16 LE, factor 0.05, offset -10.0) at CAL+0x0200
	INSERT INTO cal_maps VALUES
		(3, 'MDPS_BaseAssistCurrent_Map', 66048, '0x08010200', 'Torque Assist', '2D', 4, 3, 'uint16', 'A', 0.05, -10.0,
		 '0x08010100', 'Vehicle Speed (km/h)', '0x08010120', 'Driver Torque (Nm)', 'Primary MDPS motor assist current table', 4, 'AssistCtrl_Step');

	CREATE TABLE symbols (
		ea INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		partition_name TEXT NOT NULL,
		category TEXT,
		description TEXT
	);
	INSERT INTO symbols VALUES
		(536870912, 'g_MdpsSteerTorqueFiltered_Nm', 'SRAM', 'Sensor', 'Filtered torsion bar torque'),
		(536870916, 'g_MdpsMotorCurrentCmd_A', 'SRAM', 'Actuator', 'Target motor phase current');

	CREATE TABLE functions (
		ea INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		partition_name TEXT NOT NULL,
		end_ea INTEGER NOT NULL,
		size_bytes INTEGER NOT NULL,
		insn_count INTEGER NOT NULL,
		has_return INTEGER NOT NULL
	);
	INSERT INTO functions VALUES
		(134234112, 'AssistCtrl_Step', 'CODE', 134234144, 32, 2, 1);

	CREATE TABLE instructions (
		ea INTEGER PRIMARY KEY,
		function_ea INTEGER NOT NULL,
		partition_name TEXT NOT NULL,
		mnemonic TEXT NOT NULL,
		op_str TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		target_ea INTEGER,
		target_symbol TEXT,
		is_call INTEGER DEFAULT 0,
		call_type TEXT
	);
	INSERT INTO instructions VALUES
		(134234112, 134234112, 'CODE', 'bl', '#0x8005000', 4, 134238208, 'Interp2D_U16', 0, NULL),
		(134234116, 134234112, 'CODE', 'bx', 'lr', 2, NULL, NULL, 0, NULL);
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("seed synthetic MDPS db: %v", err)
	}
}

// populateMDPSCalBytes writes deterministic X-axis, Y-axis, and 2D AssistCurrent cells at baseOffset (0x10000 for full flash, 0x0 for CAL slice)
func populateMDPSCalBytes(buf []byte, calBase int) {
	// X Axis @ +0x0100: [0, 200, 600, 1200] * 0.1 = [0, 20, 60, 120] km/h
	xRaw := []uint16{0, 200, 600, 1200}
	for i, v := range xRaw {
		binary.LittleEndian.PutUint16(buf[calBase+0x0100+i*2:], v)
	}

	// Y Axis @ +0x0120: [-500, 0, 500] * 0.01 = [-5.0, 0.0, 5.0] Nm
	yRaw := []int16{-500, 0, 500}
	for i, v := range yRaw {
		binary.LittleEndian.PutUint16(buf[calBase+0x0120+i*2:], uint16(v))
	}

	// 2D Map (rows=3, cols=4) @ +0x0200: raw * 0.05 - 10.0
	// Row 0: raw 200 -> 0.0A, 400 -> 10.0A, 600 -> 20.0A, 800 -> 30.0A
	// Row 1: raw 200 -> 0.0A, 200 ->  0.0A, 200 ->  0.0A, 200 ->  0.0A
	// Row 2: raw 300 -> 5.0A, 500 -> 15.0A, 700 -> 25.0A, 1000 -> 40.0A
	cells := []uint16{
		200, 400, 600, 800,
		200, 200, 200, 200,
		300, 500, 700, 1000,
	}
	for i, v := range cells {
		binary.LittleEndian.PutUint16(buf[calBase+0x0200+i*2:], v)
	}
}

func doMultipartUpload(t *testing.T, handler http.Handler, endpoint, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, endpoint, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func doJSONRequest(t *testing.T, handler http.Handler, method, endpoint string, payload interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, endpoint, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestCustomMDPSDatabaseAndBinariesEndToEnd(t *testing.T) {
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir temp workspace: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	// Seed a built-in default SIM2K DB + fake 4MB SIM2K default bin so we prove switching to custom MDPS unloads SIM2K bin
	// and uploading custom MDPS bins NEVER clobbers the active custom DB back to flash_analysis.db or awd!
	defaultDBStore, err := NewDBStore("flash_analysis.db")
	if err != nil {
		t.Fatalf("init default flash_analysis.db: %v", err)
	}
	t.Cleanup(func() {
		_ = defaultDBStore.Close()
	})

	fakeSim2kBin := make([]byte, 0x400000)
	if err := os.WriteFile("IntensePop_v103_Pops-10.bin", fakeSim2kBin, 0644); err != nil {
		t.Fatalf("write fake sim2k bin: %v", err)
	}
	// Also create a dummy awd DB in workspace to ensure <=64KB custom MDPS cal slices don't hijack to AWD!
	awdDummy, err := NewDBStore("awd_95447_4J105.db")
	if err != nil {
		t.Fatalf("init dummy awd db: %v", err)
	}
	_ = awdDummy.Close()

	binMgr := NewBinManager("IntensePop_v103_Pops-10.bin")
	srv := NewServer(defaultDBStore, binMgr, "flash_analysis.db")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Build synthetic MDPS DB in staging dir
	stagingDir := t.TempDir()
	mdpsDBStaging := filepath.Join(stagingDir, "mdps_95680_3J000.db")
	buildSyntheticMDPSDB(t, mdpsDBStaging)
	mdpsDBBytes, err := os.ReadFile(mdpsDBStaging)
	if err != nil {
		t.Fatalf("read staged mdps db: %v", err)
	}

	// 1. Upload custom MDPS .db via POST /api/db/upload
	rr := doMultipartUpload(t, mux, "/api/db/upload", "mdps_95680_3J000.db", mdpsDBBytes)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/db/upload status=%d body=%s", rr.Code, rr.Body.String())
	}
	var dbUpResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &dbUpResp); err != nil {
		t.Fatalf("decode db upload resp: %v", err)
	}
	if dbUpResp["db_name"] != "mdps_95680_3J000.db" {
		t.Fatalf("expected active db_name=mdps_95680_3J000.db, got %v", dbUpResp["db_name"])
	}
	if dbUpResp["bin_loaded"] != false {
		t.Fatalf("expected stale SIM2K bin to be unloaded on custom MDPS DB switch, got bin_loaded=%v", dbUpResp["bin_loaded"])
	}

	// 2. Verify /api/stats, /api/maps/3 (metadata-only before bin upload), /api/ram/curated, /api/dids, /api/functions/0x08004000
	rrStats := doJSONRequest(t, mux, http.MethodGet, "/api/stats", nil)
	if rrStats.Code != http.StatusOK {
		t.Fatalf("GET /api/stats failed: %d %s", rrStats.Code, rrStats.Body.String())
	}
	var stats Stats
	_ = json.Unmarshal(rrStats.Body.Bytes(), &stats)
	if stats.MapCount != 3 || stats.PartitionCount != 4 {
		t.Fatalf("unexpected stats for MDPS DB: %+v", stats)
	}

	rrMapNoBin := doJSONRequest(t, mux, http.MethodGet, "/api/maps/3", nil)
	if rrMapNoBin.Code != http.StatusOK {
		t.Fatalf("GET /api/maps/3 without bin should return 200 metadata, got %d: %s", rrMapNoBin.Code, rrMapNoBin.Body.String())
	}
	var mapNoBin MapDataResponse
	_ = json.Unmarshal(rrMapNoBin.Body.Bytes(), &mapNoBin)
	if mapNoBin.BinLoaded || mapNoBin.BinError == "" || mapNoBin.Map.Name != "MDPS_BaseAssistCurrent_Map" {
		t.Fatalf("unexpected map metadata response without bin: %+v", mapNoBin)
	}

	rrRAM := doJSONRequest(t, mux, http.MethodGet, "/api/ram/curated", nil)
	var ramVars []RAMSymbol
	_ = json.Unmarshal(rrRAM.Body.Bytes(), &ramVars)
	if len(ramVars) != 2 || ramVars[0].Name != "g_MdpsSteerTorqueFiltered_Nm" {
		t.Fatalf("expected 2 MDPS SRAM symbols (not SIM2K catalog), got %+v", ramVars)
	}

	rrDIDs := doJSONRequest(t, mux, http.MethodGet, "/api/dids", nil)
	var dids []UDSDid
	_ = json.Unmarshal(rrDIDs.Body.Bytes(), &dids)
	if len(dids) != 0 {
		t.Fatalf("expected 0 SIM2K DIDs on MDPS DB without uds_dids table, got %d", len(dids))
	}

	rrFn := doJSONRequest(t, mux, http.MethodGet, "/api/functions/0x08004000", nil)
	if rrFn.Code != http.StatusOK {
		t.Fatalf("GET /api/functions/0x08004000 failed: %d %s", rrFn.Code, rrFn.Body.String())
	}
	var fnDetail FunctionDetail
	_ = json.Unmarshal(rrFn.Body.Bytes(), &fnDetail)
	if len(fnDetail.Instructions) != 2 || !fnDetail.Instructions[0].IsCall {
		t.Fatalf("expected ARM 'bl' instruction to be recognized as call, got %+v", fnDetail.Instructions)
	}

	// 3. Upload a non-standard 256 KB MDPS full flash binary via POST /api/bin/upload
	mdpsFullFlash := make([]byte, 256*1024) // 256 KB (previously rejected by hardcoded size filter)
	populateMDPSCalBytes(mdpsFullFlash, 0x10000)

	rrBin := doMultipartUpload(t, mux, "/api/bin/upload", "mdps_95680_stock_full.bin", mdpsFullFlash)
	if rrBin.Code != http.StatusOK {
		t.Fatalf("POST /api/bin/upload 256KB failed: %d %s", rrBin.Code, rrBin.Body.String())
	}
	var binUpResp map[string]interface{}
	_ = json.Unmarshal(rrBin.Body.Bytes(), &binUpResp)
	if binUpResp["db_name"] != "mdps_95680_3J000.db" {
		t.Fatalf("active custom DB was clobbered after uploading 256KB bin! got db_name=%v", binUpResp["db_name"])
	}

	// Verify /api/bin/list includes both custom DB (with MDPS category) and 256KB bin
	rrList := doJSONRequest(t, mux, http.MethodGet, "/api/bin/list", nil)
	var listResp BinListResponse
	_ = json.Unmarshal(rrList.Body.Bytes(), &listResp)
	foundMDPSDB := false
	for _, d := range listResp.DBs {
		if d.Filename == "mdps_95680_3J000.db" && d.IsCustom && d.IsActive && strings.Contains(d.Category, "MDPS") {
			foundMDPSDB = true
		}
	}
	if !foundMDPSDB {
		t.Fatalf("expected active custom MDPS DB in /api/bin/list, got %+v", listResp.DBs)
	}

	// 4. Verify live map cell + physical X/Y axis decoding from 256 KB full flash
	rrMapLive := doJSONRequest(t, mux, http.MethodGet, "/api/maps/3", nil)
	if rrMapLive.Code != http.StatusOK {
		t.Fatalf("GET /api/maps/3 with 256KB bin failed: %d %s", rrMapLive.Code, rrMapLive.Body.String())
	}
	var mapLive MapDataResponse
	_ = json.Unmarshal(rrMapLive.Body.Bytes(), &mapLive)
	if !mapLive.BinLoaded {
		t.Fatalf("expected BinLoaded=true, got false (err=%s)", mapLive.BinError)
	}
	wantX := []float64{0, 20, 60, 120}
	for i, w := range wantX {
		if i >= len(mapLive.XAxis) || math.Abs(mapLive.XAxis[i].Physical-w) > 1e-4 {
			t.Fatalf("XAxis mismatch: got %+v, want %v", mapLive.XAxis, wantX)
		}
	}
	wantY := []float64{-5, 0, 5}
	for i, w := range wantY {
		if i >= len(mapLive.YAxis) || math.Abs(mapLive.YAxis[i].Physical-w) > 1e-4 {
			t.Fatalf("YAxis mismatch: got %+v, want %v", mapLive.YAxis, wantY)
		}
	}
	if math.Abs(mapLive.Cells[2][3].Physical-40.0) > 1e-4 || math.Abs(mapLive.Cells[0][2].Physical-20.0) > 1e-4 {
		t.Fatalf("2D cell values mismatch: got %+v", mapLive.Cells)
	}

	// 5. Edit a cell and export patched binary
	newPhys := 45.0 // (45 - (-10.0)) / 0.05 = raw 1100
	rrEdit := doJSONRequest(t, mux, http.MethodPost, "/api/maps/3/edit", CellEditRequest{
		MapID: 3,
		Edits: []CellEdit{{Row: 2, Col: 3, Physical: &newPhys}},
	})
	if rrEdit.Code != http.StatusOK {
		t.Fatalf("POST /api/maps/3/edit failed: %d %s", rrEdit.Code, rrEdit.Body.String())
	}
	rrExport := doJSONRequest(t, mux, http.MethodPost, "/api/bin/export", nil)
	if rrExport.Code != http.StatusOK {
		t.Fatalf("POST /api/bin/export failed: %d", rrExport.Code)
	}
	var exportResp map[string]interface{}
	_ = json.Unmarshal(rrExport.Body.Bytes(), &exportResp)
	outPath, _ := exportResp["output_path"].(string)
	exported, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read exported file %q: %v", outPath, err)
	}
	if len(exported) != len(mdpsFullFlash) {
		t.Fatalf("exported size=%d, want %d", len(exported), len(mdpsFullFlash))
	}
	patchedRaw := binary.LittleEndian.Uint16(exported[0x10200+11*2:])
	if patchedRaw != 1100 {
		t.Fatalf("expected patched raw uint16=1100 at 0x10216, got %d", patchedRaw)
	}

	// Verify per-map revert endpoint POST /api/maps/3/reset
	rrReset := doJSONRequest(t, mux, http.MethodPost, "/api/maps/3/reset", nil)
	if rrReset.Code != http.StatusOK {
		t.Fatalf("POST /api/maps/3/reset failed: %d %s", rrReset.Code, rrReset.Body.String())
	}
	rrAfterReset := doJSONRequest(t, mux, http.MethodGet, "/api/maps/3", nil)
	var afterResetData MapDataResponse
	_ = json.Unmarshal(rrAfterReset.Body.Bytes(), &afterResetData)
	if afterResetData.IsEdited || afterResetData.Cells[2][3].IsModified || math.Abs(afterResetData.Cells[2][3].Physical-40.0) > 1e-4 {
		t.Fatalf("expected map 3 edit to be reverted to 40.0A, got %+v", afterResetData.Cells[2][3])
	}

	// 6. Upload a 64 KB MDPS calibration-only slice (.bin <= 64 KB) and verify:
	//    (a) Active DB is NOT hijacked to awd_95447_4J105.db
	//    (b) Partition-relative offset translation resolves both map (0x08010200 -> 0x0200) and axes (0x0100, 0x0120)!
	mdpsCalSlice := make([]byte, 64*1024)
	populateMDPSCalBytes(mdpsCalSlice, 0x0000)
	rrSlice := doMultipartUpload(t, mux, "/api/bin/upload", "custom_cal_slice_64k.bin", mdpsCalSlice)
	if rrSlice.Code != http.StatusOK {
		t.Fatalf("upload 64KB cal slice failed: %d %s", rrSlice.Code, rrSlice.Body.String())
	}
	var sliceResp map[string]interface{}
	_ = json.Unmarshal(rrSlice.Body.Bytes(), &sliceResp)
	if sliceResp["db_name"] != "mdps_95680_3J000.db" {
		t.Fatalf("64KB cal slice clobbered custom MDPS DB to %v!", sliceResp["db_name"])
	}
	rrSliceMap := doJSONRequest(t, mux, http.MethodGet, "/api/maps/3", nil)
	var sliceMapData MapDataResponse
	_ = json.Unmarshal(rrSliceMap.Body.Bytes(), &sliceMapData)
	if !sliceMapData.BinLoaded || math.Abs(sliceMapData.Cells[2][3].Physical-40.0) > 1e-4 || math.Abs(sliceMapData.XAxis[3].Physical-120.0) > 1e-4 {
		t.Fatalf("64KB calibration-slice map/axis resolution failed: %+v", sliceMapData)
	}

	// 7. Re-upload the SAME mdps_95680_3J000.db filename while open on Windows -> verify clean hot-reload
	rrReupload := doMultipartUpload(t, mux, "/api/db/upload", "mdps_95680_3J000.db", mdpsDBBytes)
	if rrReupload.Code != http.StatusOK {
		t.Fatalf("re-uploading active DB on Windows failed: %d %s", rrReupload.Code, rrReupload.Body.String())
	}

	// 8. Test POST /api/custom/load-path with an external folder containing a .sqlite DB + companion .bin
	extProjectDir := t.TempDir()
	extDBPath := filepath.Join(extProjectDir, "eps_gen3.sqlite")
	buildSyntheticMDPSDB(t, extDBPath)
	extBinsDir := filepath.Join(extProjectDir, "bins")
	_ = os.MkdirAll(extBinsDir, 0755)
	extBinBytes := make([]byte, 128*1024)
	populateMDPSCalBytes(extBinBytes, 0x10000)
	extBinPath := filepath.Join(extBinsDir, "eps_gen3_read.bin")
	if err := os.WriteFile(extBinPath, extBinBytes, 0644); err != nil {
		t.Fatalf("write ext bin: %v", err)
	}

	rrMount := doJSONRequest(t, mux, http.MethodPost, "/api/custom/load-path", map[string]string{
		"path": extProjectDir,
	})
	if rrMount.Code != http.StatusOK {
		t.Fatalf("POST /api/custom/load-path failed: %d %s", rrMount.Code, rrMount.Body.String())
	}
	var mountResp map[string]interface{}
	_ = json.Unmarshal(rrMount.Body.Bytes(), &mountResp)
	if mountResp["db_name"] != "eps_gen3.sqlite" || mountResp["filename"] != "eps_gen3_read.bin" || mountResp["bin_loaded"] != true {
		t.Fatalf("unexpected directory mount result: %+v", mountResp)
	}
}

func TestBuiltinWorkspaceDatabasesNoRegression(t *testing.T) {
	candidates := []string{
		"awd_95447_4J105.db",
		"tcu_cptsh_95441_4J571.db",
	}
	for _, dbName := range candidates {
		if _, err := os.Stat(dbName); err != nil {
			continue
		}
		store, err := NewDBStore(dbName)
		if err != nil {
			t.Fatalf("open builtin %s: %v", dbName, err)
		}
		stats, err := store.GetStats()
		if err != nil {
			_ = store.Close()
			t.Fatalf("%s GetStats failed: %v", dbName, err)
		}
		if stats.MapCount == 0 {
			_ = store.Close()
			t.Fatalf("expected >0 MapCount in builtin %s", dbName)
		}
		maps, total, err := store.SearchMaps("", "", "", "", 5, 0)
		_ = store.Close()
		if err != nil || total == 0 || len(maps) == 0 {
			t.Fatalf("%s SearchMaps failed: err=%v total=%d", dbName, err, total)
		}
	}
}

func TestEmbeddedStudioDesignParityAssets(t *testing.T) {
	requiredAssets := map[string][]string{
		"static/index.html": {"bars3d", "split", "map-editor-workspace", "modal-bin-selector", "db-file-input", "custom-path-input"},
		"static/app.css":    {"split-2d-3d", "editor-substrip", "active-pair-banner", "view-mode-switch"},
		"static/app.js":     {"is3DMapViewMode", "lastChosen3DStyle", "bars3d", "uploadFilesBatch", "mountCustomPath"},
		"static/icon.svg":   {"<svg"},
	}
	for assetPath, needles := range requiredAssets {
		raw, err := staticFS.ReadFile(assetPath)
		if err != nil {
			t.Fatalf("embedded asset %s missing: %v", assetPath, err)
		}
		txt := string(raw)
		for _, needle := range needles {
			if !strings.Contains(txt, needle) {
				t.Fatalf("embedded asset %s missing expected token %q", assetPath, needle)
			}
		}
	}
}
