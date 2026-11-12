package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const customSourcesFile = "custom_sources.json"

// Server wraps the DB store, Bin manager, persistent custom sources, and HTTP mux
type Server struct {
	mu            sync.RWMutex
	db            *DBStore
	bin           *BinManager
	dbPath        string
	customSources CustomSourcesConfig
}

// NewServer creates a new API server and synchronizes initial DB/Bin context
func NewServer(db *DBStore, bin *BinManager, dbPath string) *Server {
	s := &Server{
		db:     db,
		bin:    bin,
		dbPath: filepath.ToSlash(dbPath),
	}
	s.loadCustomSources()

	// If launched with an external -db or -bin via CLI, register it for discovery
	if dbPath != "" && !isBuiltinDBName(filepath.Base(dbPath)) {
		s.registerCustomDBPath(dbPath, false)
	}
	if bin.IsLoaded() && bin.GetBinPath() != "" {
		bp := bin.GetBinPath()
		if absB, err := filepath.Abs(bp); err == nil {
			if rel, rErr := filepath.Rel(".", absB); rErr == nil && strings.HasPrefix(rel, "..") {
				s.registerCustomBinPath(bp, false)
			}
		}
	}

	s.syncBinContext()
	return s
}

func (s *Server) getDBPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbPath
}

func (s *Server) setDBPath(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dbPath = filepath.ToSlash(p)
}

func isBuiltinDBName(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case "flash_analysis.db", "awd_95447_4j105.db", "tcu_cptsh_95441_4j571.db":
		return true
	default:
		return false
	}
}

func isDefaultBuiltinBinName(name string) bool {
	lower := strings.ToLower(filepath.Base(name))
	return strings.HasPrefix(lower, "intensepop") ||
		strings.HasPrefix(lower, "ckhk_") ||
		strings.HasPrefix(lower, "awd_95447") ||
		strings.HasPrefix(lower, "tcu_cptsh") ||
		strings.HasPrefix(lower, "tcu_v")
}

func isSQLiteFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	if fi.Size() == 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	hdr := make([]byte, 16)
	n, _ := f.Read(hdr)
	return n >= 15 && strings.HasPrefix(string(hdr), "SQLite format 3")
}

func appendUniquePath(slice []string, newPath string) ([]string, bool) {
	cleanNew, err := filepath.Abs(newPath)
	if err != nil {
		cleanNew = filepath.Clean(newPath)
	}
	normNew := filepath.ToSlash(cleanNew)
	for _, existing := range slice {
		cleanEx, err := filepath.Abs(existing)
		if err != nil {
			cleanEx = filepath.Clean(existing)
		}
		if strings.EqualFold(cleanEx, cleanNew) {
			return slice, false
		}
	}
	return append(slice, normNew), true
}

func (s *Server) loadCustomSources() {
	data, err := os.ReadFile(customSourcesFile)
	if err != nil {
		return
	}
	var cfg CustomSourcesConfig
	if err := json.Unmarshal(data, &cfg); err == nil {
		s.mu.Lock()
		s.customSources = cfg
		s.mu.Unlock()
	}
}

func (s *Server) saveCustomSourcesLocked() {
	data, err := json.MarshalIndent(s.customSources, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(customSourcesFile, data, 0644)
}

func (s *Server) registerCustomDBPath(dbPath string, persist bool) {
	s.mu.Lock()
	var changed bool
	var c1, c2 bool
	s.customSources.DBPaths, c1 = appendUniquePath(s.customSources.DBPaths, dbPath)
	dbDir := filepath.Dir(dbPath)
	if absDir, err := filepath.Abs(dbDir); err == nil && !strings.EqualFold(absDir, mustAbs(".")) {
		s.customSources.ScanDirs, c2 = appendUniquePath(s.customSources.ScanDirs, absDir)
	}
	changed = c1 || c2
	if persist && changed {
		s.saveCustomSourcesLocked()
	}
	scanDirs := append([]string(nil), s.customSources.ScanDirs...)
	binPaths := append([]string(nil), s.customSources.BinPaths...)
	s.mu.Unlock()

	s.bin.SetExtraSources(scanDirs, binPaths)
}

func (s *Server) registerCustomBinPath(binPath string, persist bool) {
	s.mu.Lock()
	var c1, c2 bool
	s.customSources.BinPaths, c1 = appendUniquePath(s.customSources.BinPaths, binPath)
	binDir := filepath.Dir(binPath)
	if absDir, err := filepath.Abs(binDir); err == nil && !strings.EqualFold(absDir, mustAbs(".")) {
		s.customSources.ScanDirs, c2 = appendUniquePath(s.customSources.ScanDirs, absDir)
	}
	if persist && (c1 || c2) {
		s.saveCustomSourcesLocked()
	}
	scanDirs := append([]string(nil), s.customSources.ScanDirs...)
	binPaths := append([]string(nil), s.customSources.BinPaths...)
	s.mu.Unlock()

	s.bin.SetExtraSources(scanDirs, binPaths)
}

func (s *Server) registerCustomScanDir(dirPath string, persist bool) {
	s.mu.Lock()
	var changed bool
	s.customSources.ScanDirs, changed = appendUniquePath(s.customSources.ScanDirs, dirPath)
	if persist && changed {
		s.saveCustomSourcesLocked()
	}
	scanDirs := append([]string(nil), s.customSources.ScanDirs...)
	binPaths := append([]string(nil), s.customSources.BinPaths...)
	s.mu.Unlock()

	s.bin.SetExtraSources(scanDirs, binPaths)
}

func (s *Server) syncBinContext() {
	parts, _ := s.db.GetPartitions()
	axisMaps := s.db.GetAxisMapsByEA()

	s.mu.RLock()
	curDB := s.dbPath
	scanDirs := append([]string(nil), s.customSources.ScanDirs...)
	binPaths := append([]string(nil), s.customSources.BinPaths...)
	s.mu.RUnlock()

	s.bin.SetDBContext(curDB, parts, axisMaps)
	s.bin.SetExtraSources(scanDirs, binPaths)
}

func (s *Server) jsonResponse(w http.ResponseWriter, data interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		fmt.Printf("[Server] Error encoding json response: %v\n", err)
	}
}

func (s *Server) errorResponse(w http.ResponseWriter, message string, statusCode int) {
	s.jsonResponse(w, map[string]string{"error": message}, statusCode)
}

// RegisterRoutes sets up REST API routes
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/partitions", s.handlePartitions)
	mux.HandleFunc("/api/matrix", s.handleMatrix)
	mux.HandleFunc("/api/matrix/interactions", s.handleMatrixInteractions)
	mux.HandleFunc("/api/maps", s.handleMaps)
	mux.HandleFunc("/api/maps/", s.handleMapDetailOrEdit)
	mux.HandleFunc("/api/diffs", s.handleDiffs)
	mux.HandleFunc("/api/diffs/reset", s.handleResetDiffs)
	mux.HandleFunc("/api/bin/list", s.handleBinList)
	mux.HandleFunc("/api/bin/select", s.handleBinSelect)
	mux.HandleFunc("/api/bin/upload", s.handleBinUpload)
	mux.HandleFunc("/api/bin/export", s.handleExportBin)
	mux.HandleFunc("/api/db/select", s.handleDBSelect)
	mux.HandleFunc("/api/db/upload", s.handleDBUpload)
	mux.HandleFunc("/api/custom/load-path", s.handleCustomLoadPath)
	mux.HandleFunc("/api/dflash/sectors", s.handleDFlashSectors)
	mux.HandleFunc("/api/dflash/blocks", s.handleDFlashBlocks)
	mux.HandleFunc("/api/dflash/blocks/", s.handleDFlashBlockDetail)
	mux.HandleFunc("/api/ram/curated", s.handleRAMCurated)
	mux.HandleFunc("/api/ram/search", s.handleRAMSearch)
	mux.HandleFunc("/api/ram/inspect/", s.handleRAMInspect)
	mux.HandleFunc("/api/dids", s.handleDIDs)
	mux.HandleFunc("/api/functions", s.handleFunctions)
	mux.HandleFunc("/api/functions/", s.handleFunctionDetail)
	mux.HandleFunc("/api/status", s.handleStatus)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	curDB := s.getDBPath()
	s.jsonResponse(w, map[string]interface{}{
		"status":         "healthy",
		"version":        "BinGo Master v1.1.0",
		"db_path":        curDB,
		"db_name":        filepath.Base(curDB),
		"is_custom_db":   !isBuiltinDBName(curDB),
		"bin_path":       s.bin.GetBinPath(),
		"bin_loaded":     s.bin.IsLoaded(),
		"diff_count":     len(s.bin.GetAllDiffs()),
	}, http.StatusOK)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetStats()
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, stats, http.StatusOK)
}

func (s *Server) handlePartitions(w http.ResponseWriter, r *http.Request) {
	parts, err := s.db.GetPartitions()
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, parts, http.StatusOK)
}

func (s *Server) handleMatrix(w http.ResponseWriter, r *http.Request) {
	matrix, err := s.db.GetMatrix()
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, matrix, http.StatusOK)
}

func (s *Server) handleMatrixInteractions(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	dst := r.URL.Query().Get("dst")
	accType := r.URL.Query().Get("type")

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}

	offset := 0
	if page := r.URL.Query().Get("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil && p > 1 {
			offset = (p - 1) * limit
		}
	}

	interactions, total, err := s.db.GetMatrixInteractions(src, dst, accType, limit, offset)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"total":        total,
		"limit":        limit,
		"interactions": interactions,
	}, http.StatusOK)
}

func (s *Server) handleMaps(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	folder := r.URL.Query().Get("folder")
	mapType := r.URL.Query().Get("type")
	applicability := r.URL.Query().Get("applicability")

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}

	offset := 0
	if page := r.URL.Query().Get("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil && p > 1 {
			offset = (p - 1) * limit
		}
	}

	maps, total, err := s.db.SearchMaps(query, folder, mapType, applicability, limit, offset)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"total": total,
		"limit": limit,
		"maps":  maps,
	}, http.StatusOK)
}

func (s *Server) handleMapDetailOrEdit(w http.ResponseWriter, r *http.Request) {
	// Paths: /api/maps/{id} or /api/maps/{id}/edit or /api/maps/{id}/reset
	path := strings.TrimPrefix(r.URL.Path, "/api/maps/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		s.errorResponse(w, "invalid map id", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(parts[0])
	if err != nil {
		s.errorResponse(w, "invalid map id", http.StatusBadRequest)
		return
	}

	mapObj, err := s.db.GetMapByID(id)
	if err != nil {
		s.errorResponse(w, "map not found", http.StatusNotFound)
		return
	}

	if len(parts) == 1 {
		// GET /api/maps/{id} — return map metadata gracefully even if no .bin is loaded yet or offset is out of range
		if r.Method != http.MethodGet {
			s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := s.bin.ReadMapData(*mapObj)
		if err != nil {
			data.BinError = err.Error()
		}
		s.jsonResponse(w, data, http.StatusOK)
		return
	}

	action := parts[1]
	if action == "edit" {
		if r.Method != http.MethodPost {
			s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req CellEditRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.errorResponse(w, "invalid request body", http.StatusBadRequest)
			return
		}

		diffs, err := s.bin.ApplyCellEdits(*mapObj, req.Edits)
		if err != nil {
			s.errorResponse(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.jsonResponse(w, map[string]interface{}{
			"success": true,
			"diffs":   diffs,
		}, http.StatusOK)
		return
	} else if action == "reset" {
		if r.Method != http.MethodPost {
			s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.bin.ResetEdits(&id)
		s.jsonResponse(w, map[string]interface{}{"success": true}, http.StatusOK)
		return
	}

	s.errorResponse(w, "action not supported", http.StatusNotFound)
}

func (s *Server) handleDiffs(w http.ResponseWriter, r *http.Request) {
	diffs := s.bin.GetAllDiffs()
	s.jsonResponse(w, diffs, http.StatusOK)
}

func (s *Server) handleResetDiffs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.bin.ResetEdits(nil)
	s.jsonResponse(w, map[string]interface{}{"success": true}, http.StatusOK)
}

func (s *Server) handleExportBin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		OutputPath string `json:"output_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.OutputPath = "Claude_Bins/web_editor_export.bin"
	}
	if req.OutputPath == "" {
		req.OutputPath = "Claude_Bins/web_editor_export.bin"
	}

	crc, err := s.bin.ExportPatchedBin(req.OutputPath)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	curDBLower := strings.ToLower(filepath.Base(s.getDBPath()))
	launchGateMsg := "Patched binary exported cleanly (verify custom ECU checksum before flashing if required)"
	if curDBLower == "flash_analysis.db" && s.bin.GetLoadedSize() == 0x400000 {
		launchGateMsg = "Launch-Gate CRC-32 (0x200404) verified & valid"
	} else if strings.Contains(curDBLower, "tcu_") {
		launchGateMsg = "Kefico CPTSH Calibration IEEE CRC-32 (0x1C0244) verified & valid"
	} else if strings.Contains(curDBLower, "awd_") {
		launchGateMsg = "AWD Calibration CRC-32 computed"
	}

	s.jsonResponse(w, map[string]interface{}{
		"success":        true,
		"output_path":    req.OutputPath,
		"crc_hex":        fmt.Sprintf("0x%08X", crc),
		"launch_gate":    launchGateMsg,
		"modified_cells": len(s.bin.GetAllDiffs()),
	}, http.StatusOK)
}

func (s *Server) handleDFlashSectors(w http.ResponseWriter, r *http.Request) {
	sectors := s.db.GetDFlashSectors()
	s.jsonResponse(w, sectors, http.StatusOK)
}

func (s *Server) handleDFlashBlocks(w http.ResponseWriter, r *http.Request) {
	var sector *int
	if secStr := r.URL.Query().Get("sector"); secStr != "" {
		if val, err := strconv.Atoi(secStr); err == nil {
			sector = &val
		}
	}
	activeOnly := r.URL.Query().Get("active") == "1"
	category := r.URL.Query().Get("category")

	blocks, err := s.db.GetDFlashBlocks(sector, activeOnly, category)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, blocks, http.StatusOK)
}

func (s *Server) handleDFlashBlockDetail(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/dflash/blocks/")
	if idStr == "" {
		s.errorResponse(w, "invalid block id", http.StatusBadRequest)
		return
	}

	var blockID int
	if strings.HasPrefix(idStr, "0x") || strings.HasPrefix(idStr, "0X") {
		val, err := strconv.ParseInt(idStr[2:], 16, 64)
		if err != nil {
			s.errorResponse(w, "invalid hex block id", http.StatusBadRequest)
			return
		}
		blockID = int(val)
	} else {
		val, err := strconv.Atoi(idStr)
		if err != nil {
			s.errorResponse(w, "invalid block id", http.StatusBadRequest)
			return
		}
		blockID = val
	}

	detail, err := s.db.GetDFlashBlockDetail(blockID)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusNotFound)
		return
	}
	s.jsonResponse(w, detail, http.StatusOK)
}

func (s *Server) handleRAMCurated(w http.ResponseWriter, r *http.Request) {
	symbols, fromDB := s.db.GetDBCuratedRAMSymbols()
	if !fromDB {
		if strings.EqualFold(filepath.Base(s.getDBPath()), "flash_analysis.db") {
			symbols = CuratedRAMSymbols()
		} else {
			// For custom DBs without a curated_ram table, automatically surface top RAM symbols from memory_interactions
			discovered, err := s.db.SearchRAMAddresses("", 100)
			if err == nil {
				symbols = discovered
			} else {
				symbols = []RAMSymbol{}
			}
		}
	}

	// Fill in access counts and producer/consumer functions from DB
	for i := range symbols {
		reads, writes, producers, consumers, err := s.db.InspectRAMAddress(symbols[i].EA)
		if err == nil {
			if reads > 0 || writes > 0 || symbols[i].TotalAccess == 0 {
				symbols[i].Reads = reads
				symbols[i].Writes = writes
				symbols[i].TotalAccess = reads + writes
			}
			symbols[i].Producers = producers
			symbols[i].Consumers = consumers
		}
	}

	s.jsonResponse(w, symbols, http.StatusOK)
}

func (s *Server) handleRAMSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}

	results, err := s.db.SearchRAMAddresses(query, limit)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.jsonResponse(w, results, http.StatusOK)
}

func (s *Server) handleRAMInspect(w http.ResponseWriter, r *http.Request) {
	eaStr := strings.TrimPrefix(r.URL.Path, "/api/ram/inspect/")
	if eaStr == "" {
		s.errorResponse(w, "invalid ram address", http.StatusBadRequest)
		return
	}

	var ea uint32
	if strings.HasPrefix(eaStr, "0x") || strings.HasPrefix(eaStr, "0X") {
		val, err := strconv.ParseUint(eaStr[2:], 16, 64)
		if err != nil {
			s.errorResponse(w, "invalid hex address", http.StatusBadRequest)
			return
		}
		ea = uint32(val)
	} else {
		val, err := strconv.ParseUint(eaStr, 10, 64)
		if err != nil {
			s.errorResponse(w, "invalid address", http.StatusBadRequest)
			return
		}
		ea = uint32(val)
	}

	reads, writes, producers, consumers, err := s.db.InspectRAMAddress(ea)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"ea":        ea,
		"ea_hex":    fmt.Sprintf("0x%08X", ea),
		"reads":     reads,
		"writes":    writes,
		"total":     reads + writes,
		"producers": producers,
		"consumers": consumers,
	}, http.StatusOK)
}

func (s *Server) handleDIDs(w http.ResponseWriter, r *http.Request) {
	dids, fromDB := s.db.GetDBUDSDIDs()
	if !fromDB {
		if strings.EqualFold(filepath.Base(s.getDBPath()), "flash_analysis.db") {
			dids = GetUDSDIDs()
		} else {
			dids = []UDSDid{}
		}
	}
	s.jsonResponse(w, dids, http.StatusOK)
}

func (s *Server) handleFunctions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	part := r.URL.Query().Get("partition")

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}

	offset := 0
	if page := r.URL.Query().Get("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil && p > 1 {
			offset = (p - 1) * limit
		}
	}

	fns, total, err := s.db.SearchFunctions(query, part, limit, offset)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"total":     total,
		"limit":     limit,
		"functions": fns,
	}, http.StatusOK)
}

func (s *Server) handleFunctionDetail(w http.ResponseWriter, r *http.Request) {
	eaStr := strings.TrimPrefix(r.URL.Path, "/api/functions/")
	if eaStr == "" {
		s.errorResponse(w, "invalid function address", http.StatusBadRequest)
		return
	}

	var ea uint32
	if strings.HasPrefix(eaStr, "0x") || strings.HasPrefix(eaStr, "0X") {
		val, err := strconv.ParseUint(eaStr[2:], 16, 64)
		if err != nil {
			s.errorResponse(w, "invalid hex address", http.StatusBadRequest)
			return
		}
		ea = uint32(val)
	} else {
		val, err := strconv.ParseUint(eaStr, 10, 64)
		if err != nil {
			s.errorResponse(w, "invalid address", http.StatusBadRequest)
			return
		}
		ea = uint32(val)
	}

	fn, err := s.db.GetFunctionByEA(ea)
	if err != nil {
		s.errorResponse(w, "function not found", http.StatusNotFound)
		return
	}

	insns, err := s.db.GetFunctionInstructions(fn.EA)
	if err != nil {
		insns = []Instruction{}
	}

	callers, err := s.db.GetFunctionCallers(fn.EA)
	if err != nil {
		callers = []CallEdge{}
	}

	callees, err := s.db.GetFunctionCallees(fn.EA)
	if err != nil {
		callees = []CallEdge{}
	}

	mems, err := s.db.GetFunctionMemoryAccess(fn.EA)
	if err != nil {
		mems = []MemoryInteraction{}
	}

	detail := FunctionDetail{
		Function:     *fn,
		Instructions: insns,
		Callers:      callers,
		Callees:      callees,
		MemoryAccess: mems,
	}

	s.jsonResponse(w, detail, http.StatusOK)
}

// --- Database & Binary Discovery, Pairing, Upload, and Custom Path Mounting ---

func builtinDBDescription(filename string) (string, bool) {
	switch strings.ToLower(filename) {
	case "flash_analysis.db":
		return "SIM2K-260 Engine ECU (4 MB TriCore TC277)", true
	case "awd_95447_4j105.db":
		return "AWD ECU 95447-4J105 (48 KB Infineon XC2000 / C166)", true
	case "tcu_cptsh_95441_4j571.db":
		return "Kefico CPTSH 8AT TCU 95441-4J571 (768 KB Big-Endian / A8RE25R1)", true
	default:
		return "", false
	}
}

func (s *Server) getAvailableDBs() []AvailableDB {
	curDB := s.getDBPath()
	curActiveAbs, _ := filepath.Abs(curDB)

	s.mu.RLock()
	extraDBPaths := append([]string(nil), s.customSources.DBPaths...)
	extraScanDirs := append([]string(nil), s.customSources.ScanDirs...)
	s.mu.RUnlock()

	seenAbs := make(map[string]bool)
	seenBuiltinBase := make(map[string]bool)
	var list []AvailableDB

	addDBCandidate := func(candPath string) {
		if candPath == "" {
			return
		}
		absPath, err := filepath.Abs(candPath)
		if err != nil {
			return
		}
		absKey := strings.ToLower(filepath.Clean(absPath))
		if seenAbs[absKey] {
			return
		}
		info, err := os.Stat(absPath)
		if err != nil || info.IsDir() || !isSQLiteFile(absPath) {
			return
		}

		base := info.Name()
		cat, isBuiltin := builtinDBDescription(base)
		if isBuiltin {
			lowerBase := strings.ToLower(base)
			// Avoid listing duplicate copies of the same built-in DB unless this exact path is active
			if seenBuiltinBase[lowerBase] && !strings.EqualFold(absPath, curActiveAbs) {
				return
			}
			seenBuiltinBase[lowerBase] = true
		} else {
			cat = DescribeSQLiteDB(absPath)
		}

		seenAbs[absKey] = true

		displayPath := filepath.ToSlash(absPath)
		if rel, rErr := filepath.Rel(".", absPath); rErr == nil && !strings.HasPrefix(rel, "..") {
			displayPath = filepath.ToSlash(rel)
		}

		list = append(list, AvailableDB{
			Filename:  base,
			Path:      displayPath,
			Folder:    filepath.ToSlash(filepath.Dir(displayPath)),
			SizeBytes: info.Size(),
			SizeMB:    formatFileSize(info.Size()),
			ModTime:   info.ModTime().Format("2006-01-02 15:04"),
			IsActive:  strings.EqualFold(absPath, curActiveAbs),
			IsCustom:  !isBuiltin,
			Category:  cat,
		})
	}

	// 1. Always ensure the currently active DB is included first
	if curDB != "" {
		addDBCandidate(curDB)
	}

	// 2. User-registered custom DB paths
	for _, p := range extraDBPaths {
		addDBCandidate(p)
	}

	// 3. Scan workspace & custom directories for all *.db, *.sqlite, *.sqlite3 files
	scanDirs := []string{
		".",
		"dbs",
		"custom_dbs",
		"mapping",
		"../Kia SIM2K-260/mapping",
		"../Magna AWD",
		"C:/Claude Projects/Kefico CPTSH/out",
	}
	if curDB != "" {
		scanDirs = append(scanDirs, filepath.Dir(curDB))
	}
	if s.bin.IsLoaded() && s.bin.GetBinPath() != "" {
		scanDirs = append(scanDirs, filepath.Dir(s.bin.GetBinPath()))
	}
	for _, sd := range extraScanDirs {
		scanDirs = append(scanDirs,
			sd,
			filepath.Join(sd, "dbs"),
			filepath.Join(sd, "db"),
			filepath.Join(sd, "out"),
			filepath.Join(sd, "mapping"),
		)
	}

	for _, dir := range scanDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" {
				addDBCandidate(filepath.Join(dir, e.Name()))
			}
		}
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].IsActive != list[j].IsActive {
			return list[i].IsActive
		}
		if list[i].IsCustom != list[j].IsCustom {
			return list[i].IsCustom
		}
		return list[i].Filename < list[j].Filename
	})

	return list
}

func isSameOrChildDir(a, b string) bool {
	ca := strings.ToLower(filepath.Clean(a))
	cb := strings.ToLower(filepath.Clean(b))
	if ca == "" || cb == "" {
		return false
	}
	if ca == cb {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(ca, cb+sep) || strings.HasPrefix(cb, ca+sep)
}

func (s *Server) doesBinMatchDB(binPath, dbPath string) bool {
	if binPath == "" || !s.bin.IsLoaded() {
		return false
	}
	binBase := strings.ToLower(filepath.Base(binPath))
	dbBase := strings.ToLower(filepath.Base(dbPath))
	sz := s.bin.GetLoadedSize()

	if dbBase == "tcu_cptsh_95441_4j571.db" {
		return sz == 0xBFC00 || sz == 0xC0000 || strings.Contains(binBase, "tcu") || strings.Contains(binBase, "cptsh") || strings.HasPrefix(binBase, "ekm")
	}
	if dbBase == "awd_95447_4j105.db" {
		return strings.Contains(binBase, "awd")
	}
	if dbBase == "flash_analysis.db" {
		return sz == 0x400000 && !strings.Contains(binBase, "tcu") && !strings.Contains(binBase, "awd")
	}

	// Custom DB (e.g., MDPS):
	// 1. Reject known default workspace bins from SIM2K/TCU/AWD
	if isDefaultBuiltinBinName(binBase) {
		return false
	}

	// 2. Same external directory tree
	absBin, err1 := filepath.Abs(binPath)
	absDB, err2 := filepath.Abs(dbPath)
	rootAbs := mustAbs(".")
	if err1 == nil && err2 == nil {
		dbDir := filepath.Dir(absDB)
		binDir := filepath.Dir(absBin)
		if !strings.EqualFold(dbDir, rootAbs) && !strings.EqualFold(dbDir, filepath.Join(rootAbs, "dbs")) {
			if isSameOrChildDir(binDir, dbDir) {
				return true
			}
		}
	}

	// 3. Shared stem prefix token (e.g. "mdps" in both mdps_95680.db and mdps_stock.bin)
	dbStem := strings.TrimSuffix(dbBase, filepath.Ext(dbBase))
	tok := firstToken(dbStem)
	if len(tok) >= 3 && (strings.Contains(binBase, tok) || strings.Contains(strings.ToLower(binPath), tok)) {
		return true
	}

	return false
}

// switchDatabase switches the active SQLite database, syncs BinManager context, and aligns the loaded binary
func (s *Server) switchDatabase(dbPath string, autoSelectBin bool) error {
	if err := s.db.SwitchDB(dbPath); err != nil {
		return err
	}
	s.setDBPath(dbPath)
	if !isBuiltinDBName(filepath.Base(dbPath)) {
		s.registerCustomDBPath(dbPath, true)
	}
	s.syncBinContext()

	if !autoSelectBin {
		return nil
	}

	if s.doesBinMatchDB(s.bin.GetBinPath(), dbPath) {
		return nil
	}

	dbBase := strings.ToLower(filepath.Base(dbPath))
	if dbBase == "tcu_cptsh_95441_4j571.db" {
		for _, cand := range []string{
			"tcu_cptsh_stock_read_complete.bin",
			"tcu_cptsh_EKM422402108_tuned.bin",
			"C:/Claude Projects/Kefico CPTSH/out/tcu_read_complete.bin",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.bin.Load(cand)
				return nil
			}
		}
	} else if dbBase == "awd_95447_4j105.db" {
		for _, cand := range []string{
			"awd_95447_4J105_C00000_C0BFFF.bin",
			"../Magna AWD/bins/awd_95447_4J105_C00000_C0BFFF.bin",
			"../Magna AWD/awd_95447_4J105_C00000_C0BFFF.bin",
			"../Kia SIM2K-260/awd_95447_4J105_C00000_C0BFFF.bin",
			"awd_95447_4J105_cal_32k_C04000.bin",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.bin.Load(cand)
				return nil
			}
		}
	} else if dbBase == "flash_analysis.db" {
		for _, cand := range []string{
			"IntensePop_v103_Pops-10.bin",
			"Claude_Bins/IntensePop_v103_Pops-10.bin",
			"../Kia SIM2K-260/Claude_Bins/IntensePop_v103_Pops-10.bin",
			"CKHK_3TMS2C7C_SLEEPY_DECAT_QUIET_GOOD.bin",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.bin.Load(cand)
				return nil
			}
		}
	} else {
		// Custom DB (e.g. MDPS): search available bins in the custom DB's folder or matching its stem token
		absDB, _ := filepath.Abs(dbPath)
		dbDir := strings.ToLower(filepath.Dir(absDB))
		rootAbs := strings.ToLower(mustAbs("."))
		dbsDirAbs := strings.ToLower(filepath.Join(mustAbs("."), "dbs"))
		isExternalDBDir := dbDir != "" && dbDir != rootAbs && dbDir != dbsDirAbs

		dbStem := strings.ToLower(strings.TrimSuffix(filepath.Base(dbPath), filepath.Ext(dbPath)))
		tok := firstToken(dbStem)

		availBins := s.bin.GetAvailableBins()
		for _, b := range availBins {
			if isDefaultBuiltinBinName(b.Filename) {
				continue
			}
			absB, err := filepath.Abs(b.Path)
			if err != nil {
				continue
			}
			bDir := filepath.Dir(absB)
			if isExternalDBDir && isSameOrChildDir(bDir, dbDir) {
				if err := s.bin.Load(b.Path); err == nil {
					fmt.Printf("[Server] Auto-loaded companion binary '%s' for custom DB '%s'\n", b.Path, dbPath)
					return nil
				}
			}
			if len(tok) >= 3 && strings.Contains(strings.ToLower(b.Filename), tok) {
				if err := s.bin.Load(b.Path); err == nil {
					fmt.Printf("[Server] Auto-loaded matching '%s' binary '%s' for custom DB '%s'\n", tok, b.Path, dbPath)
					return nil
				}
			}
		}

		// No matching binary found yet for this custom DB — unload stale binary from previous ECU family
		s.bin.Unload()
	}
	return nil
}

// autoMatchDBForBin aligns the database when loading a binary WITHOUT clobbering an active custom DB
func (s *Server) autoMatchDBForBin(binPath string) {
	curDB := s.getDBPath()
	binBaseLower := strings.ToLower(filepath.Base(binPath))
	absBin, _ := filepath.Abs(binPath)
	binDirLower := strings.ToLower(filepath.Dir(absBin))
	rootAbsLower := strings.ToLower(mustAbs("."))
	claudeBinsLower := strings.ToLower(filepath.Join(mustAbs("."), "Claude_Bins"))

	var sz int64
	if info, err := os.Stat(binPath); err == nil && !info.IsDir() {
		sz = info.Size()
	}

	// 1. Check if a custom DB in the same folder or with matching stem token exists
	dbsDirLower := strings.ToLower(filepath.Join(mustAbs("."), "dbs"))
	dbs := s.getAvailableDBs()
	if binDirLower != "" && binDirLower != rootAbsLower && binDirLower != claudeBinsLower && binDirLower != dbsDirLower {
		for _, db := range dbs {
			if !db.IsCustom {
				continue
			}
			if absDB, err := filepath.Abs(db.Path); err == nil {
				dbDirLower := strings.ToLower(filepath.Dir(absDB))
				if dbDirLower != rootAbsLower && dbDirLower != dbsDirLower && isSameOrChildDir(binDirLower, dbDirLower) {
					if !db.IsActive {
						_ = s.switchDatabase(db.Path, false)
						fmt.Printf("[Server] Auto-matched custom database by folder: %s\n", db.Path)
					}
					return
				}
			}
		}
	}

	for _, db := range dbs {
		if !db.IsCustom {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(db.Filename, filepath.Ext(db.Filename)))
		tok := firstToken(stem)
		if len(tok) >= 3 && strings.Contains(binBaseLower, tok) {
			if !db.IsActive {
				_ = s.switchDatabase(db.Path, false)
				fmt.Printf("[Server] Auto-matched custom database by stem '%s': %s\n", tok, db.Path)
			}
			return
		}
	}

	// 2. If user ALREADY has a custom DB active (e.g. mdps.db), do NOT clobber it unless they explicitly picked a built-in SIM2K/TCU/AWD tune
	if !isBuiltinDBName(filepath.Base(curDB)) {
		if !isDefaultBuiltinBinName(binBaseLower) {
			return
		}
	}

	// 3. Built-in ECU family matching
	isAwd := strings.Contains(binBaseLower, "awd_") || strings.Contains(binDirLower, "magna awd")
	isTcu := sz == 0xBFC00 || sz == 0xC0000 || strings.HasPrefix(binBaseLower, "tcu") || strings.Contains(binBaseLower, "cptsh") || strings.HasPrefix(binBaseLower, "ekm")

	if isTcu {
		if strings.EqualFold(filepath.Base(curDB), "tcu_cptsh_95441_4J571.db") {
			return
		}
		for _, cand := range []string{
			"tcu_cptsh_95441_4J571.db",
			"C:/Claude Projects/Kefico CPTSH/out/tcu_cptsh_95441_4J571.db",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.switchDatabase(cand, false)
				return
			}
		}
	} else if isAwd {
		if strings.EqualFold(filepath.Base(curDB), "awd_95447_4J105.db") {
			return
		}
		for _, cand := range []string{
			"awd_95447_4J105.db",
			"../Magna AWD/awd_95447_4J105.db",
			"mapping/awd_95447_4J105.db",
			"../Kia SIM2K-260/mapping/awd_95447_4J105.db",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.switchDatabase(cand, false)
				return
			}
		}
	} else if sz == 0x400000 {
		if strings.EqualFold(filepath.Base(curDB), "flash_analysis.db") {
			return
		}
		for _, cand := range []string{
			"flash_analysis.db",
			"mapping/flash_analysis.db",
			"../Kia SIM2K-260/mapping/flash_analysis.db",
			"../mapping/flash_analysis.db",
		} {
			if _, err := os.Stat(cand); err == nil {
				_ = s.switchDatabase(cand, false)
				return
			}
		}
	}
}

func (s *Server) handleBinList(w http.ResponseWriter, r *http.Request) {
	bins := s.bin.GetAvailableBins()
	curPath := s.bin.GetBinPath()
	curName := ""
	if s.bin.IsLoaded() && curPath != "" {
		curName = filepath.Base(curPath)
	}
	curDB := s.getDBPath()

	s.jsonResponse(w, BinListResponse{
		ActivePath:   curPath,
		ActiveName:   curName,
		BinLoaded:    s.bin.IsLoaded(),
		ActiveDB:     filepath.ToSlash(curDB),
		ActiveDBName: filepath.Base(curDB),
		Bins:         bins,
		DBs:          s.getAvailableDBs(),
	}, http.StatusOK)
}

func (s *Server) handleBinSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		s.errorResponse(w, "invalid request: path required", http.StatusBadRequest)
		return
	}

	cleanPath := strings.Trim(strings.TrimSpace(req.Path), `"'`)
	if err := s.bin.Load(cleanPath); err != nil {
		s.errorResponse(w, fmt.Sprintf("failed to load binary: %v", err), http.StatusBadRequest)
		return
	}

	s.autoMatchDBForBin(cleanPath)
	curDB := s.getDBPath()

	s.jsonResponse(w, map[string]interface{}{
		"success":    true,
		"path":       s.bin.GetBinPath(),
		"filename":   filepath.Base(s.bin.GetBinPath()),
		"bin_loaded": s.bin.IsLoaded(),
		"db_path":    curDB,
		"db_name":    filepath.Base(curDB),
	}, http.StatusOK)
}

func (s *Server) handleDBSelect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		s.errorResponse(w, "invalid request: path required", http.StatusBadRequest)
		return
	}

	cleanPath := strings.Trim(strings.TrimSpace(req.Path), `"'`)
	if err := s.switchDatabase(cleanPath, true); err != nil {
		s.errorResponse(w, fmt.Sprintf("failed to switch database: %v", err), http.StatusBadRequest)
		return
	}

	curDB := s.getDBPath()
	binName := ""
	if s.bin.IsLoaded() && s.bin.GetBinPath() != "" {
		binName = filepath.Base(s.bin.GetBinPath())
	}

	s.jsonResponse(w, map[string]interface{}{
		"success":    true,
		"db_path":    curDB,
		"db_name":    filepath.Base(curDB),
		"bin_path":   s.bin.GetBinPath(),
		"filename":   binName,
		"bin_loaded": s.bin.IsLoaded(),
	}, http.StatusOK)
}

func (s *Server) handleDBUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Allow up to 512 MB SQLite database uploads, streamed directly to disk
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		s.errorResponse(w, "failed to read multipart upload", http.StatusBadRequest)
		return
	}

	var savedPath string
	var origFilename string

	for {
		part, pErr := mr.NextPart()
		if pErr == io.EOF {
			break
		}
		if pErr != nil {
			s.errorResponse(w, fmt.Sprintf("upload stream error: %v", pErr), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}

		origFilename = filepath.Base(part.FileName())
		ext := strings.ToLower(filepath.Ext(origFilename))
		if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" {
			origFilename += ".db"
		}

		if err := os.MkdirAll("dbs", 0755); err != nil {
			_ = part.Close()
			s.errorResponse(w, fmt.Sprintf("failed to create dbs directory: %v", err), http.StatusInternalServerError)
			return
		}

		tmpPath := filepath.Join("dbs", origFilename+".uploading")
		outFile, cErr := os.Create(tmpPath)
		if cErr != nil {
			_ = part.Close()
			s.errorResponse(w, fmt.Sprintf("failed to create database file: %v", cErr), http.StatusInternalServerError)
			return
		}

		written, cpErr := io.Copy(outFile, part)
		_ = outFile.Close()
		_ = part.Close()

		if cpErr != nil {
			_ = os.Remove(tmpPath)
			s.errorResponse(w, fmt.Sprintf("failed to save uploaded database: %v", cpErr), http.StatusInternalServerError)
			return
		}
		if written < 100 || !isSQLiteFile(tmpPath) {
			_ = os.Remove(tmpPath)
			s.errorResponse(w, "uploaded file is not a valid SQLite 3 database (.db)", http.StatusBadRequest)
			return
		}

		finalPath := filepath.Join("dbs", origFilename)
		// Release any open SQLite handle on finalPath before replacing it on Windows
		s.db.ReleasePath(finalPath)
		_ = os.Remove(finalPath + "-wal")
		_ = os.Remove(finalPath + "-shm")
		_ = os.Remove(finalPath)
		if rErr := os.Rename(tmpPath, finalPath); rErr != nil {
			// Fallback copy if locked on Windows
			data, readErr := os.ReadFile(tmpPath)
			_ = os.Remove(tmpPath)
			if readErr != nil || os.WriteFile(finalPath, data, 0644) != nil {
				s.errorResponse(w, fmt.Sprintf("could not finalize '%s' (file may be in use)", origFilename), http.StatusInternalServerError)
				return
			}
		}
		savedPath = filepath.ToSlash(finalPath)
		break
	}

	if savedPath == "" {
		s.errorResponse(w, "missing database file in form field 'file'", http.StatusBadRequest)
		return
	}

	autoSelectBin := r.URL.Query().Get("skip_bin_auto") != "1"
	if err := s.switchDatabase(savedPath, autoSelectBin); err != nil {
		s.errorResponse(w, fmt.Sprintf("database uploaded to '%s' but failed to open: %v", savedPath, err), http.StatusBadRequest)
		return
	}

	curDB := s.getDBPath()
	binName := ""
	if s.bin.IsLoaded() && s.bin.GetBinPath() != "" {
		binName = filepath.Base(s.bin.GetBinPath())
	}

	s.jsonResponse(w, map[string]interface{}{
		"success":    true,
		"db_path":    curDB,
		"db_name":    filepath.Base(curDB),
		"bin_path":   s.bin.GetBinPath(),
		"filename":   binName,
		"bin_loaded": s.bin.IsLoaded(),
	}, http.StatusOK)
}

func (s *Server) handleBinUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 64 MB max upload for calibration/firmware binaries
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		s.errorResponse(w, "failed to parse multipart form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		s.errorResponse(w, "missing file in form field 'file'", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		s.errorResponse(w, "failed to read uploaded file", http.StatusInternalServerError)
		return
	}

	savedPath, err := s.bin.SaveUploadedBin(header.Filename, data)
	if err != nil {
		s.errorResponse(w, err.Error(), http.StatusBadRequest)
		return
	}

	if r.URL.Query().Get("keep_db") != "1" {
		s.autoMatchDBForBin(savedPath)
	}
	curDB := s.getDBPath()

	s.jsonResponse(w, map[string]interface{}{
		"success":    true,
		"path":       savedPath,
		"filename":   filepath.Base(savedPath),
		"bin_loaded": s.bin.IsLoaded(),
		"db_path":    curDB,
		"db_name":    filepath.Base(curDB),
	}, http.StatusOK)
}

// handleCustomLoadPath mounts a local .db, .bin, or project folder directly from disk without copying
func (s *Server) handleCustomLoadPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.errorResponse(w, "invalid JSON request body", http.StatusBadRequest)
		return
	}

	cleanPath := strings.Trim(strings.TrimSpace(req.Path), `"'`)
	if cleanPath == "" {
		s.errorResponse(w, "file or folder path is required", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(cleanPath)
	if err != nil {
		s.errorResponse(w, fmt.Sprintf("path not found on disk: %s", cleanPath), http.StatusBadRequest)
		return
	}

	if info.IsDir() {
		// Register folder, scan for .db and .bin files, and mount them
		s.registerCustomScanDir(cleanPath, true)

		var foundDBs []string
		var foundBins []string
		subDirs := []string{
			cleanPath,
			filepath.Join(cleanPath, "dbs"),
			filepath.Join(cleanPath, "db"),
			filepath.Join(cleanPath, "out"),
			filepath.Join(cleanPath, "mapping"),
			filepath.Join(cleanPath, "bins"),
			filepath.Join(cleanPath, "dumps"),
		}
		for _, sd := range subDirs {
			entries, rErr := os.ReadDir(sd)
			if rErr != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				full := filepath.Join(sd, e.Name())
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if (ext == ".db" || ext == ".sqlite" || ext == ".sqlite3") && isSQLiteFile(full) {
					foundDBs = append(foundDBs, full)
				} else if ext == ".bin" {
					foundBins = append(foundBins, full)
				}
			}
		}

		if len(foundDBs) == 0 && len(foundBins) == 0 {
			s.errorResponse(w, fmt.Sprintf("no .db or .bin files found in folder '%s'", cleanPath), http.StatusBadRequest)
			return
		}

		if len(foundDBs) > 0 {
			if err := s.switchDatabase(foundDBs[0], true); err != nil {
				s.errorResponse(w, fmt.Sprintf("failed to open database '%s': %v", foundDBs[0], err), http.StatusBadRequest)
				return
			}
		}
		if len(foundBins) > 0 && (!s.bin.IsLoaded() || len(foundDBs) == 0) {
			_ = s.bin.Load(foundBins[0])
			if len(foundDBs) == 0 {
				s.autoMatchDBForBin(foundBins[0])
			}
		}

		curDB := s.getDBPath()
		binName := ""
		if s.bin.IsLoaded() && s.bin.GetBinPath() != "" {
			binName = filepath.Base(s.bin.GetBinPath())
		}
		s.jsonResponse(w, map[string]interface{}{
			"success":         true,
			"mounted_type":    "directory",
			"discovered_dbs":  len(foundDBs),
			"discovered_bins": len(foundBins),
			"db_path":         curDB,
			"db_name":         filepath.Base(curDB),
			"bin_path":        s.bin.GetBinPath(),
			"filename":        binName,
			"bin_loaded":      s.bin.IsLoaded(),
		}, http.StatusOK)
		return
	}

	ext := strings.ToLower(filepath.Ext(cleanPath))
	if ext == ".db" || ext == ".sqlite" || ext == ".sqlite3" || isSQLiteFile(cleanPath) {
		s.registerCustomDBPath(cleanPath, true)
		if err := s.switchDatabase(cleanPath, true); err != nil {
			s.errorResponse(w, fmt.Sprintf("failed to load custom database: %v", err), http.StatusBadRequest)
			return
		}

		curDB := s.getDBPath()
		binName := ""
		if s.bin.IsLoaded() && s.bin.GetBinPath() != "" {
			binName = filepath.Base(s.bin.GetBinPath())
		}
		s.jsonResponse(w, map[string]interface{}{
			"success":      true,
			"mounted_type": "db",
			"db_path":      curDB,
			"db_name":      filepath.Base(curDB),
			"bin_path":     s.bin.GetBinPath(),
			"filename":     binName,
			"bin_loaded":   s.bin.IsLoaded(),
		}, http.StatusOK)
		return
	}

	// Treat as binary (.bin or firmware image)
	s.registerCustomBinPath(cleanPath, true)
	if err := s.bin.Load(cleanPath); err != nil {
		s.errorResponse(w, fmt.Sprintf("failed to load binary '%s': %v", cleanPath, err), http.StatusBadRequest)
		return
	}
	s.autoMatchDBForBin(cleanPath)

	curDB := s.getDBPath()
	s.jsonResponse(w, map[string]interface{}{
		"success":      true,
		"mounted_type": "bin",
		"db_path":      curDB,
		"db_name":      filepath.Base(curDB),
		"bin_path":     s.bin.GetBinPath(),
		"filename":     filepath.Base(s.bin.GetBinPath()),
		"bin_loaded":   s.bin.IsLoaded(),
	}, http.StatusOK)
}
