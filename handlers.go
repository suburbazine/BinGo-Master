package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

// Server wraps the DB store, Bin manager, and HTTP mux
type Server struct {
	db     *DBStore
	bin    *BinManager
	dbPath string
}

// NewServer creates a new API server
func NewServer(db *DBStore, bin *BinManager, dbPath string) *Server {
	return &Server{
		db:     db,
		bin:    bin,
		dbPath: dbPath,
	}
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
	s.jsonResponse(w, map[string]interface{}{
		"status":     "healthy",
		"version":    "BinGo Master v1.0.0",
		"db_path":    s.dbPath,
		"bin_path":   s.bin.GetBinPath(),
		"bin_loaded": s.bin.IsLoaded(),
		"diff_count": len(s.bin.GetAllDiffs()),
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
		// GET /api/maps/{id}
		if r.Method != http.MethodGet {
			s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := s.bin.ReadMapData(*mapObj)
		if err != nil {
			s.errorResponse(w, err.Error(), http.StatusInternalServerError)
			return
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

	s.jsonResponse(w, map[string]interface{}{
		"success":       true,
		"output_path":   req.OutputPath,
		"crc_hex":       fmt.Sprintf("0x%08X", crc),
		"launch_gate":   "Launch-Gate CRC-32 (0x200404) verified & valid",
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
	symbols := CuratedRAMSymbols()

	// Fill in access counts and producer/consumer functions from DB
	for i := range symbols {
		reads, writes, producers, consumers, err := s.db.InspectRAMAddress(symbols[i].EA)
		if err == nil {
			symbols[i].Reads = reads
			symbols[i].Writes = writes
			symbols[i].TotalAccess = reads + writes
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
	dids := GetUDSDIDs()
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

func (s *Server) handleBinList(w http.ResponseWriter, r *http.Request) {
	bins := s.bin.GetAvailableBins()
	curPath := s.bin.GetBinPath()
	curName := filepath.Base(curPath)

	s.jsonResponse(w, BinListResponse{
		ActivePath: curPath,
		ActiveName: curName,
		Bins:       bins,
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		s.errorResponse(w, "invalid request: path required", http.StatusBadRequest)
		return
	}

	if err := s.bin.Load(req.Path); err != nil {
		s.errorResponse(w, fmt.Sprintf("failed to load binary: %v", err), http.StatusBadRequest)
		return
	}

	s.jsonResponse(w, map[string]interface{}{
		"success":  true,
		"path":     s.bin.GetBinPath(),
		"filename": filepath.Base(s.bin.GetBinPath()),
	}, http.StatusOK)
}

func (s *Server) handleBinUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.errorResponse(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 10 MB max upload
	if err := r.ParseMultipartForm(10 << 20); err != nil {
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

	s.jsonResponse(w, map[string]interface{}{
		"success":  true,
		"path":     savedPath,
		"filename": filepath.Base(savedPath),
	}, http.StatusOK)
}

