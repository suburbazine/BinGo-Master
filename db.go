package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DBStore wraps SQLite queries for ECU calibration & disassembly analysis databases
type DBStore struct {
	mu      sync.RWMutex
	db      *sql.DB
	dbPath  string
	retired []*sql.DB
}

// NewDBStore opens connection to an analysis SQLite database or initializes an empty schema if none exists
func NewDBStore(dbPath string) (*DBStore, error) {
	_, statErr := os.Stat(dbPath)
	var db *sql.DB
	var err error

	if statErr != nil {
		// Database doesn't exist yet: open read-write to initialize empty schema
		db, err = sql.Open("sqlite3", dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create sqlite database '%s': %w", dbPath, err)
		}
		if err := initSchema(db); err != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w", err)
		}
	} else {
		// Existing database: open with shared cache
		db, err = sql.Open("sqlite3", dbPath+"?cache=shared")
		if err != nil {
			return nil, fmt.Errorf("failed to open sqlite database '%s': %w", dbPath, err)
		}
	}

	// Verify connection and SQLite validity
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	var dummy int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&dummy); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("file '%s' is not a valid SQLite database: %w", dbPath, err)
	}

	return &DBStore{db: db, dbPath: dbPath}, nil
}

func initSchema(db *sql.DB) error {
	schemaDDL := `
	CREATE TABLE IF NOT EXISTS partitions (
		id INTEGER PRIMARY KEY,
		name TEXT UNIQUE NOT NULL,
		start_ea INTEGER NOT NULL,
		end_ea INTEGER NOT NULL,
		file_start INTEGER,
		file_end INTEGER,
		memory_type TEXT DEFAULT 'FLASH',
		description TEXT,
		color TEXT
	);
	CREATE TABLE IF NOT EXISTS symbols (
		ea INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		partition_name TEXT NOT NULL,
		category TEXT NOT NULL,
		description TEXT
	);
	CREATE TABLE IF NOT EXISTS functions (
		ea INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		partition_name TEXT NOT NULL,
		end_ea INTEGER NOT NULL,
		size_bytes INTEGER NOT NULL,
		insn_count INTEGER NOT NULL,
		has_return INTEGER NOT NULL,
		caller_count INTEGER DEFAULT 0,
		callee_count INTEGER DEFAULT 0,
		mem_read_count INTEGER DEFAULT 0,
		mem_write_count INTEGER DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS instructions (
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
	CREATE TABLE IF NOT EXISTS calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		caller_ea INTEGER NOT NULL,
		caller_fn INTEGER NOT NULL,
		caller_partition TEXT NOT NULL,
		callee_ea INTEGER NOT NULL,
		callee_fn INTEGER DEFAULT 0,
		callee_fn_name TEXT DEFAULT '',
		callee_partition TEXT NOT NULL,
		call_type TEXT NOT NULL,
		is_cross_partition INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS memory_interactions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		insn_ea INTEGER NOT NULL,
		function_ea INTEGER NOT NULL,
		source_partition TEXT NOT NULL,
		target_ea INTEGER NOT NULL,
		target_partition TEXT NOT NULL,
		access_type TEXT NOT NULL,
		access_width INTEGER NOT NULL,
		target_symbol TEXT,
		mnemonic TEXT NOT NULL,
		op_str TEXT NOT NULL,
		is_cross_partition INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS cal_maps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		file_offset INTEGER NOT NULL,
		ea INTEGER NOT NULL,
		folder TEXT,
		map_type TEXT,
		columns INTEGER,
		rows INTEGER,
		data_org TEXT,
		unit TEXT,
		factor REAL,
		offset REAL,
		axis_x_ea INTEGER,
		axis_x_name TEXT,
		axis_y_ea INTEGER,
		axis_y_name TEXT,
		comment TEXT,
		ref_count INTEGER DEFAULT 0,
		ref_functions TEXT,
		applicability TEXT DEFAULT 'LIVE_ACTUATOR',
		applicability_note TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS dflash_blocks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sector_idx INTEGER NOT NULL,
		block_id INTEGER NOT NULL,
		offset INTEGER NOT NULL,
		dflash_ea INTEGER DEFAULT 0,
		version_count INTEGER NOT NULL,
		is_active INTEGER DEFAULT 1,
		length INTEGER NOT NULL,
		crc INTEGER NOT NULL,
		name TEXT NOT NULL,
		category TEXT NOT NULL,
		description TEXT,
		payload_hex TEXT,
		payload_ascii TEXT
	);
	`
	_, err := db.Exec(schemaDDL)
	return err
}

func (s *DBStore) conn() *sql.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

func (s *DBStore) getDBPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbPath
}

// Close closes the active database connection and any retired handles immediately
func (s *DBStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.retired {
		if r != nil {
			_ = r.Close()
		}
	}
	s.retired = nil
	if s.db != nil {
		err := s.db.Close()
		s.db = nil
		return err
	}
	return nil
}

// ReleasePath closes the underlying SQLite handle if it matches targetPath (needed before overwriting an open DB file on Windows)
func (s *DBStore) ReleasePath(targetPath string) {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.retired {
		if r != nil {
			_ = r.Close()
		}
	}
	s.retired = nil
	if s.db != nil && s.dbPath != "" {
		if absCur, cErr := filepath.Abs(s.dbPath); cErr == nil && strings.EqualFold(absCur, absTarget) {
			_ = s.db.Close()
			s.db = nil
		}
	}
}

// SwitchDB opens and validates a new SQLite database at dbPath and atomically swaps it in
func (s *DBStore) SwitchDB(dbPath string) error {
	info, err := os.Stat(dbPath)
	if err != nil {
		return fmt.Errorf("database file not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("path '%s' is a directory, expected a .db file", dbPath)
	}
	if info.Size() > 0 {
		f, fErr := os.Open(dbPath)
		if fErr == nil {
			hdr := make([]byte, 16)
			n, _ := f.Read(hdr)
			_ = f.Close()
			if n >= 15 && !strings.HasPrefix(string(hdr), "SQLite format 3") {
				return fmt.Errorf("file '%s' does not have a valid SQLite 3 header", dbPath)
			}
		}
	}

	newDB, err := sql.Open("sqlite3", dbPath+"?cache=shared")
	if err != nil {
		return fmt.Errorf("failed to open sqlite database '%s': %w", dbPath, err)
	}
	if err := newDB.Ping(); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("failed to connect to database '%s': %w", dbPath, err)
	}
	var tableCnt int
	if err := newDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCnt); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("invalid SQLite database '%s': %w", dbPath, err)
	}

	s.mu.Lock()
	oldDB := s.db
	s.db = newDB
	s.dbPath = dbPath
	if oldDB != nil {
		s.retired = append(s.retired, oldDB)
	}
	s.mu.Unlock()

	if oldDB != nil {
		time.AfterFunc(200*time.Millisecond, func() {
			_ = oldDB.Close()
			s.mu.Lock()
			filtered := s.retired[:0]
			for _, r := range s.retired {
				if r != oldDB {
					filtered = append(filtered, r)
				}
			}
			s.retired = filtered
			s.mu.Unlock()
		})
	}
	return nil
}

// --- Schema Introspection Helpers ---

func (s *DBStore) hasTable(table string) bool {
	var cnt int
	err := s.conn().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND LOWER(name) = LOWER(?)", table).Scan(&cnt)
	return err == nil && cnt > 0
}

func (s *DBStore) tableColumns(table string) map[string]bool {
	cols := make(map[string]bool)
	rows, err := s.conn().Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return cols
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil {
			cols[strings.ToLower(name)] = true
		}
	}
	return cols
}

func pickCol(cols map[string]bool, candidates ...string) string {
	for _, c := range candidates {
		if cols[strings.ToLower(c)] {
			return c
		}
	}
	return ""
}

func colStr(cols map[string]bool, prefix, fallback string, candidates ...string) string {
	c := pickCol(cols, candidates...)
	escaped := strings.ReplaceAll(fallback, "'", "''")
	if c == "" {
		return fmt.Sprintf("'%s'", escaped)
	}
	if prefix != "" {
		return fmt.Sprintf("COALESCE(CAST(%s.%s AS TEXT), '%s')", prefix, c, escaped)
	}
	return fmt.Sprintf("COALESCE(CAST(%s AS TEXT), '%s')", c, escaped)
}

func colInt(cols map[string]bool, prefix string, fallback int, candidates ...string) string {
	c := pickCol(cols, candidates...)
	if c == "" {
		return strconv.Itoa(fallback)
	}
	if prefix != "" {
		return fmt.Sprintf("COALESCE(%s.%s, %d)", prefix, c, fallback)
	}
	return fmt.Sprintf("COALESCE(%s, %d)", c, fallback)
}

func colFloat(cols map[string]bool, prefix string, fallback float64, candidates ...string) string {
	c := pickCol(cols, candidates...)
	if c == "" {
		return fmt.Sprintf("%g", fallback)
	}
	if prefix != "" {
		return fmt.Sprintf("COALESCE(%s.%s, %g)", prefix, c, fallback)
	}
	return fmt.Sprintf("COALESCE(%s, %g)", c, fallback)
}

func parseFlexInt64(v interface{}) (int64, bool) {
	if v == nil {
		return 0, false
	}
	switch val := v.(type) {
	case int64:
		return val, true
	case int:
		return int64(val), true
	case float64:
		return int64(val), true
	case []byte:
		return parseFlexStringInt(string(val))
	case string:
		return parseFlexStringInt(val)
	default:
		return parseFlexStringInt(fmt.Sprintf("%v", val))
	}
}

func parseFlexStringInt(str string) (int64, bool) {
	s := strings.TrimSpace(str)
	if s == "" {
		return 0, false
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if u, err := strconv.ParseUint(s[2:], 16, 64); err == nil {
			return int64(u), true
		}
	}
	if strings.HasPrefix(s, "$") {
		if u, err := strconv.ParseUint(s[1:], 16, 64); err == nil {
			return int64(u), true
		}
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f), true
	}
	return 0, false
}

func (s *DBStore) safeCount(query string, args ...interface{}) int {
	var cnt int
	if err := s.conn().QueryRow(query, args...).Scan(&cnt); err != nil {
		return 0
	}
	return cnt
}

// GetStats returns summary counts safely across full or partial custom schemas
func (s *DBStore) GetStats() (Stats, error) {
	var st Stats
	if s.hasTable("instructions") {
		st.InstructionCount = s.safeCount("SELECT COUNT(*) FROM instructions")
	}
	if s.hasTable("functions") {
		st.FunctionCount = s.safeCount("SELECT COUNT(*) FROM functions")
	}
	if s.hasTable("calls") {
		st.CallCount = s.safeCount("SELECT COUNT(*) FROM calls")
	}
	if s.hasTable("memory_interactions") {
		st.MemoryInteractions = s.safeCount("SELECT COUNT(*) FROM memory_interactions")
	}
	if s.hasTable("dflash_blocks") {
		st.DFlashBlockCount = s.safeCount("SELECT COUNT(*) FROM dflash_blocks")
	}
	if s.hasTable("symbols") {
		st.SymbolCount = s.safeCount("SELECT COUNT(*) FROM symbols")
	}
	if s.hasTable("partitions") {
		st.PartitionCount = s.safeCount("SELECT COUNT(*) FROM partitions")
	}
	if s.hasTable("cal_maps") {
		st.MapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps")
		cols := s.tableColumns("cal_maps")
		if cols["applicability"] {
			st.LiveMapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps WHERE COALESCE(applicability, 'LIVE_ACTUATOR') = 'LIVE_ACTUATOR'")
			st.DeadMapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('DEAD_INACTIVE', 'IDENTITY')")
			st.TorqueMapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('TORQUE_MODEL_REF', 'AXIS')")
			st.GatedMapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('GATED_DISABLED', 'DIAGNOSTIC')")
		} else {
			st.LiveMapCount = st.MapCount
		}
		folderCol := pickCol(cols, "folder", "category", "subsystem", "group_name")
		if folderCol != "" && cols["applicability"] {
			st.CustomOSMapCount = s.safeCount(fmt.Sprintf(
				"SELECT COUNT(*) FROM cal_maps WHERE applicability = 'CUSTOM_OS' OR %s LIKE '00 - Xtremission Custom OS%%' OR %s LIKE '00B - FrostByte%%'",
				folderCol, folderCol,
			))
		} else if folderCol != "" {
			st.CustomOSMapCount = s.safeCount(fmt.Sprintf(
				"SELECT COUNT(*) FROM cal_maps WHERE %s LIKE '00 - Xtremission Custom OS%%' OR %s LIKE '00B - FrostByte%%'",
				folderCol, folderCol,
			))
		} else if cols["applicability"] {
			st.CustomOSMapCount = s.safeCount("SELECT COUNT(*) FROM cal_maps WHERE applicability = 'CUSTOM_OS'")
		}
	}
	return st, nil
}

// GetPartitions returns all flash/RAM partitions with instruction and function counts
func (s *DBStore) GetPartitions() ([]Partition, error) {
	if !s.hasTable("partitions") {
		return []Partition{}, nil
	}
	cols := s.tableColumns("partitions")
	hasFns := s.hasTable("functions") && s.tableColumns("functions")["partition_name"]
	hasInsns := s.hasTable("instructions") && s.tableColumns("instructions")["partition_name"]

	idExpr := colInt(cols, "p", 0, "id", "rowid")
	nameExpr := colStr(cols, "p", "PARTITION", "name", "partition_name")
	startExpr := "p." + pickCol(cols, "start_ea", "base_ea", "start_addr")
	if startExpr == "p." {
		startExpr = "0"
	}
	endExpr := "p." + pickCol(cols, "end_ea", "end_addr")
	if endExpr == "p." {
		endExpr = "0"
	}
	fStartCol := pickCol(cols, "file_start", "file_offset", "rom_start")
	fStartExpr := "NULL"
	if fStartCol != "" {
		fStartExpr = "p." + fStartCol
	}
	fEndCol := pickCol(cols, "file_end", "rom_end")
	fEndExpr := "NULL"
	if fEndCol != "" {
		fEndExpr = "p." + fEndCol
	}
	memTypeExpr := colStr(cols, "p", "FLASH", "memory_type", "type", "category")
	descExpr := colStr(cols, "p", "", "description", "comment", "notes")

	fnCountExpr := "0"
	if hasFns && cols["name"] {
		fnCountExpr = "(SELECT COUNT(*) FROM functions f WHERE f.partition_name = p.name)"
	}
	insnCountExpr := "0"
	if hasInsns && cols["name"] {
		insnCountExpr = "(SELECT COUNT(*) FROM instructions i WHERE i.partition_name = p.name)"
	}
	orderExpr := "1"
	if cols["id"] {
		orderExpr = "p.id"
	} else if cols["start_ea"] {
		orderExpr = "p.start_ea"
	}

	query := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s, %s, %s, %s, %s, %s
		FROM partitions p
		ORDER BY %s
	`, idExpr, nameExpr, startExpr, endExpr, fStartExpr, fEndExpr, memTypeExpr, descExpr, fnCountExpr, insnCountExpr, orderExpr)

	rows, err := s.conn().Query(query)
	if err != nil {
		return []Partition{}, nil
	}
	defer rows.Close()

	var list []Partition
	idx := 1
	for rows.Next() {
		var p Partition
		var rawStart, rawEnd, rawFStart, rawFEnd interface{}
		err := rows.Scan(
			&p.ID, &p.Name, &rawStart, &rawEnd, &rawFStart, &rawFEnd,
			&p.MemoryType, &p.Description, &p.FnCount, &p.InsnCount,
		)
		if err != nil {
			continue
		}
		if p.ID == 0 {
			p.ID = idx
		}
		idx++
		if v, ok := parseFlexInt64(rawStart); ok {
			p.StartEA = uint32(v)
		}
		if v, ok := parseFlexInt64(rawEnd); ok {
			p.EndEA = uint32(v)
		}
		if v, ok := parseFlexInt64(rawFStart); ok {
			iv := int(v)
			p.FileStart = &iv
		}
		if v, ok := parseFlexInt64(rawFEnd); ok {
			iv := int(v)
			p.FileEnd = &iv
		}
		p.StartEAHex = fmt.Sprintf("0x%08X", p.StartEA)
		p.EndEAHex = fmt.Sprintf("0x%08X", p.EndEA)
		if p.EndEA >= p.StartEA && (p.EndEA > 0 || p.StartEA > 0) {
			szBytes := uint64(p.EndEA) - uint64(p.StartEA) + 1
			p.SizeKB = int(szBytes / 1024)
			if p.SizeKB == 0 && szBytes > 0 {
				p.SizeKB = 1
			}
		}
		list = append(list, p)
	}
	if list == nil {
		list = []Partition{}
	}
	return list, nil
}

// GetMatrix returns cross-partition interaction counts
func (s *DBStore) GetMatrix() ([]MatrixCell, error) {
	if !s.hasTable("memory_interactions") {
		return []MatrixCell{}, nil
	}
	cols := s.tableColumns("memory_interactions")
	if !cols["source_partition"] || !cols["target_partition"] {
		return []MatrixCell{}, nil
	}
	whereClause := "WHERE source_partition != target_partition"
	if cols["is_cross_partition"] {
		whereClause = "WHERE is_cross_partition = 1"
	}

	rows, err := s.conn().Query(fmt.Sprintf(`
		SELECT COALESCE(source_partition, ''), COALESCE(target_partition, ''), COUNT(*) as cnt
		FROM memory_interactions
		%s
		GROUP BY source_partition, target_partition
		ORDER BY cnt DESC
	`, whereClause))
	if err != nil {
		return []MatrixCell{}, nil
	}
	defer rows.Close()

	cells := make([]MatrixCell, 0)
	for rows.Next() {
		var c MatrixCell
		if err := rows.Scan(&c.SourcePartition, &c.TargetPartition, &c.Count); err == nil {
			cells = append(cells, c)
		}
	}
	return cells, nil
}

// GetMatrixInteractions returns boundary traffic between two partitions
func (s *DBStore) GetMatrixInteractions(src, dst, accType string, limit, offset int) ([]MemoryInteraction, int, error) {
	if !s.hasTable("memory_interactions") {
		return []MemoryInteraction{}, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	cols := s.tableColumns("memory_interactions")
	whereClause := "WHERE m.source_partition != m.target_partition"
	if cols["is_cross_partition"] {
		whereClause = "WHERE m.is_cross_partition = 1"
	}
	var args []interface{}

	if src != "" && src != "ALL" {
		whereClause += " AND m.source_partition = ?"
		args = append(args, src)
	}
	if dst != "" && dst != "ALL" {
		whereClause += " AND m.target_partition = ?"
		args = append(args, dst)
	}
	if accType != "" && accType != "ALL" && cols["access_type"] {
		whereClause += " AND m.access_type = ?"
		args = append(args, accType)
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM memory_interactions m %s", whereClause)
	total := s.safeCount(countQuery, args...)

	hasFns := s.hasTable("functions")
	fnJoin := ""
	fnNameExpr := "''"
	if hasFns {
		fnJoin = "LEFT JOIN functions f ON m.function_ea = f.ea"
		fnNameExpr = "COALESCE(f.name, '')"
	}
	crossExpr := "1"
	if cols["is_cross_partition"] {
		crossExpr = "COALESCE(m.is_cross_partition, 1)"
	}

	query := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s,
		       %s, %s, %s, %s,
		       %s, %s, %s, %s
		FROM memory_interactions m
		%s
		%s
		ORDER BY m.insn_ea
		LIMIT ? OFFSET ?
	`,
		colInt(cols, "m", 0, "id", "rowid"),
		colInt(cols, "m", 0, "insn_ea"),
		colInt(cols, "m", 0, "function_ea"),
		fnNameExpr,
		colStr(cols, "m", "", "source_partition"),
		colInt(cols, "m", 0, "target_ea"),
		colStr(cols, "m", "", "target_partition"),
		colStr(cols, "m", "READ", "access_type"),
		colInt(cols, "m", 4, "access_width"),
		colStr(cols, "m", "", "target_symbol"),
		colStr(cols, "m", "", "mnemonic"),
		colStr(cols, "m", "", "op_str"),
		crossExpr,
		fnJoin,
		whereClause,
	)

	queryArgs := append(args, limit, offset)
	rows, err := s.conn().Query(query, queryArgs...)
	if err != nil {
		return []MemoryInteraction{}, 0, nil
	}
	defer rows.Close()

	list := make([]MemoryInteraction, 0)
	for rows.Next() {
		var mi MemoryInteraction
		var isCrossInt int
		err := rows.Scan(
			&mi.ID, &mi.InsnEA, &mi.FunctionEA, &mi.FunctionName, &mi.SourcePartition,
			&mi.TargetEA, &mi.TargetPartition, &mi.AccessType, &mi.AccessWidth,
			&mi.TargetSymbol, &mi.Mnemonic, &mi.OpStr, &isCrossInt,
		)
		if err != nil {
			continue
		}
		mi.InsnEAHex = fmt.Sprintf("0x%08X", mi.InsnEA)
		mi.FunctionEAHex = fmt.Sprintf("0x%08X", mi.FunctionEA)
		mi.TargetEAHex = fmt.Sprintf("0x%08X", mi.TargetEA)
		mi.IsCross = isCrossInt == 1
		list = append(list, mi)
	}
	return list, total, nil
}

func (s *DBStore) buildCalMapSelectCols(cols map[string]bool) string {
	idExpr := colInt(cols, "", 0, "id", "rowid")
	nameExpr := colStr(cols, "", "Unnamed_Map", "name", "map_name", "symbol")

	offCol := pickCol(cols, "file_offset", "rom_offset", "bin_offset")
	offExpr := "0"
	if offCol != "" {
		offExpr = offCol
	}

	eaCol := pickCol(cols, "ea", "address", "virtual_ea", "addr")
	eaExpr := "0"
	if eaCol != "" {
		eaExpr = eaCol
	}

	folderExpr := colStr(cols, "", "Root", "folder", "category", "subsystem", "group_name")
	typeExpr := colStr(cols, "", "2D_MAP", "map_type", "type")
	colsExpr := colInt(cols, "", 1, "columns", "cols", "width", "dim_x")
	rowsExpr := colInt(cols, "", 1, "rows", "height", "dim_y")
	orgExpr := colStr(cols, "", "uint16", "data_org", "data_type", "dtype")
	unitExpr := colStr(cols, "", "", "unit", "units")
	factorExpr := colFloat(cols, "", 1.0, "factor", "scale", "scaling")
	offsetExpr := colFloat(cols, "", 0.0, "offset", "bias", "add_offset")

	axXCol := pickCol(cols, "axis_x_ea", "x_axis_ea")
	axXExpr := "NULL"
	if axXCol != "" {
		axXExpr = axXCol
	}
	axXNameExpr := colStr(cols, "", "", "axis_x_name", "x_axis_name")

	axYCol := pickCol(cols, "axis_y_ea", "y_axis_ea")
	axYExpr := "NULL"
	if axYCol != "" {
		axYExpr = axYCol
	}
	axYNameExpr := colStr(cols, "", "", "axis_y_name", "y_axis_name")

	commentExpr := colStr(cols, "", "", "comment", "description", "notes")
	refCntExpr := colInt(cols, "", 0, "ref_count", "xref_count")
	refFnsExpr := colStr(cols, "", "", "ref_functions", "calling_functions", "xrefs")
	appExpr := colStr(cols, "", "LIVE_ACTUATOR", "applicability")
	appNoteExpr := colStr(cols, "", "", "applicability_note")

	return fmt.Sprintf(
		"%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s",
		idExpr, nameExpr, offExpr, eaExpr, folderExpr, typeExpr, colsExpr, rowsExpr,
		orgExpr, unitExpr, factorExpr, offsetExpr, axXExpr, axXNameExpr, axYExpr, axYNameExpr,
		commentExpr, refCntExpr, refFnsExpr, appExpr, appNoteExpr,
	)
}

func scanCalMapRow(scanner interface {
	Scan(dest ...interface{}) error
}) (CalMap, error) {
	var m CalMap
	var rawOff, rawEA, rawAxX, rawAxY interface{}
	err := scanner.Scan(
		&m.ID, &m.Name, &rawOff, &rawEA, &m.Folder, &m.MapType,
		&m.Columns, &m.Rows, &m.DataOrg, &m.Unit, &m.Factor, &m.Offset,
		&rawAxX, &m.AxisXName, &rawAxY, &m.AxisYName, &m.Comment,
		&m.RefCount, &m.RefFunctions,
		&m.Applicability, &m.ApplicabilityNote,
	)
	if err != nil {
		return m, err
	}
	if v, ok := parseFlexInt64(rawOff); ok {
		m.FileOffset = int(v)
	}
	if v, ok := parseFlexInt64(rawEA); ok {
		m.EA = uint32(v)
	}
	if v, ok := parseFlexInt64(rawAxX); ok && v > 0 {
		u := uint32(v)
		m.AxisXEA = &u
		m.AxisXEAHex = fmt.Sprintf("0x%08X", u)
	}
	if v, ok := parseFlexInt64(rawAxY); ok && v > 0 {
		u := uint32(v)
		m.AxisYEA = &u
		m.AxisYEAHex = fmt.Sprintf("0x%08X", u)
	}
	if m.Columns < 1 {
		m.Columns = 1
	}
	if m.Rows < 1 {
		m.Rows = 1
	}
	if m.Factor == 0 {
		m.Factor = 1.0
	}
	m.FileOffsetHex = fmt.Sprintf("$%06X", m.FileOffset)
	m.EAHex = fmt.Sprintf("0x%08X", m.EA)
	if m.RefFunctions != "" {
		parts := strings.Split(m.RefFunctions, ",")
		for _, p := range parts {
			trimmed := strings.TrimSpace(p)
			if trimmed != "" {
				m.RefFnList = append(m.RefFnList, trimmed)
			}
		}
	}
	return m, nil
}

// SearchMaps queries calibration maps by name, folder, file offset, EA, or applicability
func (s *DBStore) SearchMaps(query, folder, mapType, applicability string, limit, offset int) ([]CalMap, int, error) {
	if !s.hasTable("cal_maps") {
		return []CalMap{}, 0, nil
	}
	if limit <= 0 {
		limit = 250
	}
	if limit > 5000 {
		limit = 5000
	}

	cols := s.tableColumns("cal_maps")
	offCol := pickCol(cols, "file_offset", "rom_offset", "bin_offset")
	eaCol := pickCol(cols, "ea", "address", "virtual_ea", "addr")
	nameCol := pickCol(cols, "name", "map_name", "symbol")
	folderCol := pickCol(cols, "folder", "category", "subsystem", "group_name")
	commentCol := pickCol(cols, "comment", "description", "notes")
	typeCol := pickCol(cols, "map_type", "type")

	whereParts := []string{"1=1"}
	var args []interface{}

	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil {
				var sub []string
				if eaCol != "" {
					sub = append(sub, fmt.Sprintf("%s = ? OR LOWER(CAST(%s AS TEXT)) = LOWER(?)", eaCol, eaCol))
					args = append(args, val, q)
				}
				if offCol != "" {
					sub = append(sub, fmt.Sprintf("%s = ?", offCol))
					args = append(args, val)
				}
				if len(sub) > 0 {
					whereParts = append(whereParts, "("+strings.Join(sub, " OR ")+")")
				}
			}
		} else if strings.HasPrefix(q, "$") {
			if val, err := strconv.ParseUint(q[1:], 16, 64); err == nil {
				var sub []string
				if offCol != "" {
					sub = append(sub, fmt.Sprintf("%s = ?", offCol))
					args = append(args, val)
				}
				if eaCol != "" {
					sub = append(sub, fmt.Sprintf("%s = ? OR %s = ?", eaCol, eaCol))
					args = append(args, val, 0x80800000+(val-0x200000))
				}
				if len(sub) > 0 {
					whereParts = append(whereParts, "("+strings.Join(sub, " OR ")+")")
				}
			}
		} else {
			pattern := "%" + q + "%"
			var sub []string
			if nameCol != "" {
				sub = append(sub, fmt.Sprintf("%s LIKE ?", nameCol))
				args = append(args, pattern)
			}
			if folderCol != "" {
				sub = append(sub, fmt.Sprintf("%s LIKE ?", folderCol))
				args = append(args, pattern)
			}
			if commentCol != "" {
				sub = append(sub, fmt.Sprintf("%s LIKE ?", commentCol))
				args = append(args, pattern)
			}
			if len(sub) > 0 {
				whereParts = append(whereParts, "("+strings.Join(sub, " OR ")+")")
			}
		}
	}

	if folder != "" && folder != "ALL" && folderCol != "" {
		whereParts = append(whereParts, fmt.Sprintf("%s LIKE ?", folderCol))
		args = append(args, "%"+folder+"%")
	}

	if mapType != "" && mapType != "ALL" && typeCol != "" {
		switch mapType {
		case "eZweidim":
			whereParts = append(whereParts, fmt.Sprintf("UPPER(%s) IN ('EZWEIDIM', '2D_MAP', '3D_MAP', '2D', '3D', 'MAP', 'SURFACE', 'TABLE')", typeCol))
		case "eEindim":
			whereParts = append(whereParts, fmt.Sprintf("UPPER(%s) IN ('EEINDIM', '1D_CURVE', 'AXIS', '1D', 'CURVE', 'ARRAY', 'LOOKUP_1D')", typeCol))
		case "eEinzel":
			whereParts = append(whereParts, fmt.Sprintf("UPPER(%s) IN ('EEINZEL', 'SCALAR', 'ID_STRING', 'CONST', 'CONSTANT', 'VALUE', 'SINGLE', 'PARAM')", typeCol))
		default:
			whereParts = append(whereParts, fmt.Sprintf("%s = ?", typeCol))
			args = append(args, mapType)
		}
	}

	if applicability != "" && applicability != "ALL" {
		if applicability == "CUSTOM_OS" {
			var cosClauses []string
			if cols["applicability"] {
				cosClauses = append(cosClauses, "applicability = 'CUSTOM_OS'")
			}
			if folderCol != "" {
				cosClauses = append(cosClauses, fmt.Sprintf("%s LIKE '00 - Xtremission Custom OS%%' OR %s LIKE '00B - FrostByte%%'", folderCol, folderCol))
			}
			if len(cosClauses) == 0 {
				return []CalMap{}, 0, nil
			}
			whereParts = append(whereParts, "("+strings.Join(cosClauses, " OR ")+")")
		} else if cols["applicability"] {
			switch applicability {
			case "DEAD_INACTIVE":
				whereParts = append(whereParts, "applicability IN ('DEAD_INACTIVE', 'IDENTITY')")
			case "TORQUE_MODEL_REF":
				whereParts = append(whereParts, "applicability IN ('TORQUE_MODEL_REF', 'AXIS')")
			case "GATED_DISABLED":
				whereParts = append(whereParts, "applicability IN ('GATED_DISABLED', 'DIAGNOSTIC')")
			default:
				whereParts = append(whereParts, "COALESCE(applicability, 'LIVE_ACTUATOR') = ?")
				args = append(args, applicability)
			}
		} else if applicability != "LIVE_ACTUATOR" {
			return []CalMap{}, 0, nil
		}
	}

	whereClause := strings.Join(whereParts, " AND ")
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM cal_maps WHERE %s", whereClause)
	total := s.safeCount(countQuery, args...)

	orderClause := "1 ASC"
	if eaCol != "" {
		orderClause = eaCol + " ASC"
	} else if offCol != "" {
		orderClause = offCol + " ASC"
	}
	if cols["applicability"] {
		orderClause = fmt.Sprintf(`
			CASE COALESCE(applicability, 'LIVE_ACTUATOR')
				WHEN 'LIVE_ACTUATOR' THEN 0
				WHEN 'GATED_DISABLED' THEN 1
				WHEN 'DIAGNOSTIC' THEN 2
				WHEN 'DEAD_INACTIVE' THEN 3
				WHEN 'IDENTITY' THEN 4
				ELSE 5
			END, %s`, orderClause)
	}

	sqlQuery := fmt.Sprintf(`
		SELECT %s
		FROM cal_maps
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, s.buildCalMapSelectCols(cols), whereClause, orderClause)

	queryArgs := append(args, limit, offset)
	rows, err := s.conn().Query(sqlQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	maps := make([]CalMap, 0)
	for rows.Next() {
		m, err := scanCalMapRow(rows)
		if err == nil {
			maps = append(maps, m)
		}
	}

	return maps, total, nil
}

// GetMapByID retrieves a single calibration map
func (s *DBStore) GetMapByID(id int) (*CalMap, error) {
	if !s.hasTable("cal_maps") {
		return nil, fmt.Errorf("cal_maps table not present in active database")
	}
	cols := s.tableColumns("cal_maps")
	idCol := pickCol(cols, "id")
	if idCol == "" {
		idCol = "rowid"
	}

	query := fmt.Sprintf(`
		SELECT %s
		FROM cal_maps
		WHERE %s = ?
	`, s.buildCalMapSelectCols(cols), idCol)

	row := s.conn().QueryRow(query, id)
	m, err := scanCalMapRow(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// GetAxisMapsByEA returns a lookup of maps keyed by EA for physical axis scaling in custom DBs
func (s *DBStore) GetAxisMapsByEA() map[uint32]CalMap {
	res := make(map[uint32]CalMap)
	if !s.hasTable("cal_maps") {
		return res
	}
	cols := s.tableColumns("cal_maps")
	if pickCol(cols, "ea", "address", "virtual_ea", "addr") == "" {
		return res
	}
	rows, err := s.conn().Query(fmt.Sprintf("SELECT %s FROM cal_maps", s.buildCalMapSelectCols(cols)))
	if err != nil {
		return res
	}
	defer rows.Close()
	for rows.Next() {
		if m, err := scanCalMapRow(rows); err == nil && m.EA > 0 {
			// Prefer explicit AXIS / 1D entries if duplicate EAs exist
			if existing, exists := res[m.EA]; !exists || strings.EqualFold(m.Applicability, "AXIS") || strings.Contains(strings.ToUpper(m.MapType), "AXIS") || existing.Rows > 1 {
				res[m.EA] = m
			}
		}
	}
	return res
}

// GetDFlashSectors returns sector summary dynamically adapted to the active database
func (s *DBStore) GetDFlashSectors() []DFlashSector {
	hasDFlash := s.hasTable("dflash_blocks")
	dCols := s.tableColumns("dflash_blocks")
	secCol := pickCol(dCols, "sector_idx", "sector")
	actCol := pickCol(dCols, "is_active")

	var s0Count, s0Active, s1Count, s1Active int
	if hasDFlash && secCol != "" {
		actExpr := "COUNT(*)"
		if actCol != "" {
			actExpr = fmt.Sprintf("COALESCE(SUM(%s), 0)", actCol)
		}
		_ = s.conn().QueryRow(fmt.Sprintf("SELECT COUNT(*), %s FROM dflash_blocks WHERE %s = 0", actExpr, secCol)).Scan(&s0Count, &s0Active)
		_ = s.conn().QueryRow(fmt.Sprintf("SELECT COUNT(*), %s FROM dflash_blocks WHERE %s = 1", actExpr, secCol)).Scan(&s1Count, &s1Active)
	}

	var isAWD int
	if s.hasTable("partitions") {
		_ = s.conn().QueryRow("SELECT COUNT(*) FROM partitions WHERE name = 'CAL_DPP1'").Scan(&isAWD)
	}
	if isAWD > 0 {
		return []DFlashSector{
			{
				SectorIndex:  1,
				Name:         "Sector 1 (C166 RAM & NVRAM Shadow: DPP3/DPP0)",
				Status:       "ACTIVE (Live 16 KB RAM & EEPROM Shadow 0xC00000..0xC03FFF)",
				StartOffset:  0x00000,
				EndOffset:    0x03FFF,
				SizeKB:       16,
				BlockCount:   s1Count,
				ActiveBlocks: s1Active,
			},
			{
				SectorIndex:  0,
				Name:         "Sector 0 (Flash Identity & Diagnostic Blocks: DPP1/DPP2)",
				Status:       "CONST (32 KB Calibration & UDS/DTC Flash 0xC04000..0xC0BFFF)",
				StartOffset:  0x04000,
				EndOffset:    0x0BFFF,
				SizeKB:       32,
				BlockCount:   s0Count,
				ActiveBlocks: s0Active,
			},
		}
	}

	lowerPath := strings.ToLower(s.getDBPath())
	isSIM2K := strings.Contains(lowerPath, "flash_analysis.db")
	if isSIM2K {
		return []DFlashSector{
			{
				SectorIndex:  1,
				Name:         "Sector 1 (Active Ping-Pong)",
				Status:       "ACTIVE (Live Calibration State)",
				StartOffset:  0x18000,
				EndOffset:    0x2FFFF,
				SizeKB:       96,
				BlockCount:   s1Count,
				ActiveBlocks: s1Active,
			},
			{
				SectorIndex:  0,
				Name:         "Sector 0 (Passive Ping-Pong)",
				Status:       "PASSIVE (Previous Generation Shadow)",
				StartOffset:  0x00000,
				EndOffset:    0x17FFF,
				SizeKB:       96,
				BlockCount:   s0Count,
				ActiveBlocks: s0Active,
			},
		}
	}

	// Custom / TCU DB: build sector cards dynamically from dflash_blocks if present
	if !hasDFlash || secCol == "" {
		return []DFlashSector{}
	}
	var totalBlocks int
	_ = s.conn().QueryRow("SELECT COUNT(*) FROM dflash_blocks").Scan(&totalBlocks)
	if totalBlocks == 0 {
		return []DFlashSector{}
	}

	actExpr := "COUNT(*)"
	if actCol != "" {
		actExpr = fmt.Sprintf("COALESCE(SUM(%s), 0)", actCol)
	}
	offCol := pickCol(dCols, "offset")
	minOffExpr, maxOffExpr := "0", "0"
	if offCol != "" {
		minOffExpr = fmt.Sprintf("COALESCE(MIN(%s), 0)", offCol)
		maxOffExpr = fmt.Sprintf("COALESCE(MAX(%s + COALESCE(length, 0)), 0)", offCol)
	}

	rows, err := s.conn().Query(fmt.Sprintf(`
		SELECT %s, COUNT(*), %s, %s, %s
		FROM dflash_blocks
		GROUP BY %s
		ORDER BY %s DESC
	`, secCol, actExpr, minOffExpr, maxOffExpr, secCol, secCol))
	if err != nil {
		return []DFlashSector{}
	}
	defer rows.Close()

	var sectors []DFlashSector
	for rows.Next() {
		var secIdx, cnt, act, minOff, maxOff int
		if err := rows.Scan(&secIdx, &cnt, &act, &minOff, &maxOff); err == nil {
			szKB := (maxOff - minOff + 1023) / 1024
			if szKB < 1 {
				szKB = 1
			}
			status := "NVM / Data Flash Sector"
			if act > 0 {
				status = "ACTIVE (NVM / EEPROM / Identity Blocks)"
			}
			sectors = append(sectors, DFlashSector{
				SectorIndex:  secIdx,
				Name:         fmt.Sprintf("Sector %d (NVM / Data Flash)", secIdx),
				Status:       status,
				StartOffset:  minOff,
				EndOffset:    maxOff,
				SizeKB:       szKB,
				BlockCount:   cnt,
				ActiveBlocks: act,
			})
		}
	}
	return sectors
}

func (s *DBStore) queryDFlashBlocksWhere(whereClause string, orderClause string, args ...interface{}) ([]DFlashBlock, error) {
	if !s.hasTable("dflash_blocks") {
		return []DFlashBlock{}, nil
	}
	cols := s.tableColumns("dflash_blocks")
	query := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s, %s,
		       %s, %s, %s, %s, %s, %s,
		       %s, %s
		FROM dflash_blocks
		WHERE %s
		ORDER BY %s
	`,
		colInt(cols, "", 0, "id", "rowid"),
		colInt(cols, "", 0, "sector_idx", "sector"),
		colInt(cols, "", 0, "block_id"),
		colInt(cols, "", 0, "offset"),
		colInt(cols, "", 0, "dflash_ea", "ea"),
		colInt(cols, "", 1, "version_count", "generation"),
		colInt(cols, "", 1, "is_active"),
		colInt(cols, "", 0, "length", "size_bytes"),
		colInt(cols, "", 0, "crc"),
		colStr(cols, "", "Block", "name"),
		colStr(cols, "", "SYSTEM", "category"),
		colStr(cols, "", "", "description", "comment"),
		colStr(cols, "", "", "payload_hex"),
		colStr(cols, "", "", "payload_ascii"),
		whereClause,
		orderClause,
	)

	rows, err := s.conn().Query(query, args...)
	if err != nil {
		return []DFlashBlock{}, nil
	}
	defer rows.Close()

	blocks := make([]DFlashBlock, 0)
	for rows.Next() {
		var b DFlashBlock
		var isActiveInt int
		err := rows.Scan(
			&b.ID, &b.SectorIdx, &b.BlockID, &b.Offset, &b.DFlashEA, &b.VersionCount,
			&isActiveInt, &b.Length, &b.CRC, &b.Name, &b.Category, &b.Description,
			&b.PayloadHex, &b.PayloadASCII,
		)
		if err != nil {
			continue
		}
		b.IsActive = isActiveInt == 1
		b.BlockIDHex = fmt.Sprintf("0x%02X", b.BlockID)
		b.OffsetHex = fmt.Sprintf("0x%05X", b.Offset)
		b.DFlashEAHex = fmt.Sprintf("0x%08X", b.DFlashEA)
		b.CRCHex = fmt.Sprintf("0x%04X", b.CRC)
		blocks = append(blocks, b)
	}
	return blocks, nil
}

// GetDFlashBlocks queries blocks inventory
func (s *DBStore) GetDFlashBlocks(sector *int, activeOnly bool, category string) ([]DFlashBlock, error) {
	if !s.hasTable("dflash_blocks") {
		return []DFlashBlock{}, nil
	}
	cols := s.tableColumns("dflash_blocks")
	secCol := pickCol(cols, "sector_idx", "sector")
	actCol := pickCol(cols, "is_active")
	catCol := pickCol(cols, "category")

	whereParts := []string{"1=1"}
	var args []interface{}

	if sector != nil && secCol != "" {
		whereParts = append(whereParts, fmt.Sprintf("%s = ?", secCol))
		args = append(args, *sector)
	}
	if activeOnly && actCol != "" {
		whereParts = append(whereParts, fmt.Sprintf("%s = 1", actCol))
	}
	if category != "" && category != "ALL" && catCol != "" {
		whereParts = append(whereParts, fmt.Sprintf("%s = ?", catCol))
		args = append(args, category)
	}

	orderParts := []string{"1"}
	if cols["block_id"] {
		orderParts = []string{"block_id"}
		if secCol != "" {
			orderParts = append(orderParts, secCol+" DESC")
		}
		if vCol := pickCol(cols, "version_count", "generation"); vCol != "" {
			orderParts = append(orderParts, vCol+" DESC")
		}
	}

	return s.queryDFlashBlocksWhere(strings.Join(whereParts, " AND "), strings.Join(orderParts, ", "), args...)
}

// GetDFlashBlockDetail returns block detail and all historical revisions
func (s *DBStore) GetDFlashBlockDetail(blockID int) (*DFlashBlockDetail, error) {
	if !s.hasTable("dflash_blocks") {
		return nil, fmt.Errorf("dflash_blocks table not present")
	}
	cols := s.tableColumns("dflash_blocks")
	idCol := pickCol(cols, "block_id", "id", "rowid")
	var orderParts []string
	if secCol := pickCol(cols, "sector_idx", "sector"); secCol != "" {
		orderParts = append(orderParts, secCol+" DESC")
	}
	if vCol := pickCol(cols, "version_count", "generation"); vCol != "" {
		orderParts = append(orderParts, vCol+" DESC")
	}
	if cols["offset"] {
		orderParts = append(orderParts, "offset DESC")
	}
	if len(orderParts) == 0 {
		orderParts = []string{"1"}
	}

	list, err := s.queryDFlashBlocksWhere(fmt.Sprintf("%s = ?", idCol), strings.Join(orderParts, ", "), blockID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("block 0x%02X not found", blockID)
	}

	active := list[0]
	for _, b := range list {
		if b.IsActive && b.SectorIdx == 1 {
			active = b
			break
		}
	}

	return &DFlashBlockDetail{
		ActiveBlock: active,
		Revisions:   list,
	}, nil
}

// InspectRAMAddress queries access frequency and producer/consumer functions for a RAM EA
func (s *DBStore) InspectRAMAddress(targetEA uint32) (int, int, []FnLink, []FnLink, error) {
	if !s.hasTable("memory_interactions") {
		return 0, 0, []FnLink{}, []FnLink{}, nil
	}
	hasFns := s.hasTable("functions")
	fnJoin := ""
	fnNameExpr := "''"
	if hasFns {
		fnJoin = "LEFT JOIN functions f ON m.function_ea = f.ea"
		fnNameExpr = "COALESCE(f.name, '')"
	}

	prodRows, err := s.conn().Query(fmt.Sprintf(`
		SELECT DISTINCT COALESCE(m.function_ea, 0), %s, COALESCE(m.source_partition, ''),
		                COALESCE(m.insn_ea, 0), COALESCE(m.access_type, 'WRITE'), COALESCE(m.mnemonic, ''), COALESCE(m.op_str, '')
		FROM memory_interactions m
		%s
		WHERE m.target_ea = ? AND UPPER(m.access_type) IN ('WRITE', 'MODIFY', 'STORE')
		ORDER BY m.insn_ea
		LIMIT 50
	`, fnNameExpr, fnJoin), targetEA)
	if err != nil {
		return 0, 0, []FnLink{}, []FnLink{}, nil
	}
	defer prodRows.Close()

	var producers []FnLink
	for prodRows.Next() {
		var fl FnLink
		if err := prodRows.Scan(&fl.FunctionEA, &fl.FunctionName, &fl.Partition, &fl.InsnEA, &fl.AccessType, &fl.Mnemonic, &fl.OpStr); err == nil {
			fl.FunctionEAHex = fmt.Sprintf("0x%08X", fl.FunctionEA)
			fl.InsnEAHex = fmt.Sprintf("0x%08X", fl.InsnEA)
			producers = append(producers, fl)
		}
	}

	consRows, err := s.conn().Query(fmt.Sprintf(`
		SELECT DISTINCT COALESCE(m.function_ea, 0), %s, COALESCE(m.source_partition, ''),
		                COALESCE(m.insn_ea, 0), COALESCE(m.access_type, 'READ'), COALESCE(m.mnemonic, ''), COALESCE(m.op_str, '')
		FROM memory_interactions m
		%s
		WHERE m.target_ea = ? AND UPPER(m.access_type) IN ('READ', 'ADDR_LOAD', 'LOAD', 'LEA')
		ORDER BY m.insn_ea
		LIMIT 50
	`, fnNameExpr, fnJoin), targetEA)
	if err != nil {
		return 0, 0, producers, []FnLink{}, nil
	}
	defer consRows.Close()

	var consumers []FnLink
	for consRows.Next() {
		var fl FnLink
		if err := consRows.Scan(&fl.FunctionEA, &fl.FunctionName, &fl.Partition, &fl.InsnEA, &fl.AccessType, &fl.Mnemonic, &fl.OpStr); err == nil {
			fl.FunctionEAHex = fmt.Sprintf("0x%08X", fl.FunctionEA)
			fl.InsnEAHex = fmt.Sprintf("0x%08X", fl.InsnEA)
			consumers = append(consumers, fl)
		}
	}

	reads := s.safeCount("SELECT COUNT(*) FROM memory_interactions WHERE target_ea = ? AND UPPER(access_type) IN ('READ', 'ADDR_LOAD', 'LOAD', 'LEA')", targetEA)
	writes := s.safeCount("SELECT COUNT(*) FROM memory_interactions WHERE target_ea = ? AND UPPER(access_type) IN ('WRITE', 'MODIFY', 'STORE')", targetEA)

	return reads, writes, producers, consumers, nil
}

// SearchRAMAddresses finds top accessed RAM addresses matching query across any ECU RAM partition
func (s *DBStore) SearchRAMAddresses(query string, limit int) ([]RAMSymbol, error) {
	if !s.hasTable("memory_interactions") {
		return []RAMSymbol{}, nil
	}
	if limit <= 0 {
		limit = 50
	}

	ramPartFilter := "(target_partition IN ('DSPR', 'LMU', 'RAM_DPP3', 'RAM_DPP0') OR UPPER(target_partition) LIKE '%RAM%' OR UPPER(target_partition) LIKE '%SRAM%' OR UPPER(target_partition) LIKE '%IRAM%'"
	if s.hasTable("partitions") && s.tableColumns("partitions")["memory_type"] {
		ramPartFilter += " OR target_partition IN (SELECT name FROM partitions WHERE UPPER(memory_type) LIKE '%RAM%')"
	}
	ramPartFilter += ")"

	whereClause := "WHERE " + ramPartFilter
	var args []interface{}
	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil {
				whereClause += " AND target_ea = ?"
				args = append(args, val)
			}
		} else {
			whereClause += " AND (COALESCE(target_symbol, '') LIKE ? OR COALESCE(op_str, '') LIKE ?)"
			args = append(args, "%"+q+"%", "%"+q+"%")
		}
	}

	querySQL := fmt.Sprintf(`
		SELECT target_ea, COALESCE(target_partition, 'RAM'), COUNT(*) as cnt,
		       SUM(CASE WHEN UPPER(access_type) IN ('READ', 'ADDR_LOAD', 'LOAD', 'LEA') THEN 1 ELSE 0 END) as r_cnt,
		       SUM(CASE WHEN UPPER(access_type) IN ('WRITE', 'MODIFY', 'STORE') THEN 1 ELSE 0 END) as w_cnt
		FROM memory_interactions
		%s
		GROUP BY target_ea
		ORDER BY cnt DESC
		LIMIT ?
	`, whereClause)

	args = append(args, limit)
	rows, err := s.conn().Query(querySQL, args...)
	if err != nil {
		return []RAMSymbol{}, nil
	}
	defer rows.Close()

	hasCurated := s.hasTable("curated_ram")
	hasSymbols := s.hasTable("symbols")
	list := make([]RAMSymbol, 0)
	for rows.Next() {
		var ea uint32
		var part string
		var total, reads, writes int
		if err := rows.Scan(&ea, &part, &total, &reads, &writes); err == nil {
			name := fmt.Sprintf("RAM_%08X", ea)
			desc := fmt.Sprintf("Active RAM variable at 0x%08X (%d reads, %d writes)", ea, reads, writes)
			dtype := "uint32"
			if hasCurated {
				_ = s.conn().QueryRow("SELECT COALESCE(name, ''), COALESCE(data_type, 'uint32'), COALESCE(description, '') FROM curated_ram WHERE ea = ?", ea).Scan(&name, &dtype, &desc)
			} else if hasSymbols {
				_ = s.conn().QueryRow("SELECT COALESCE(name, ''), COALESCE(description, '') FROM symbols WHERE ea = ?", ea).Scan(&name, &desc)
			}
			sym := RAMSymbol{
				EA:          ea,
				EAHex:       fmt.Sprintf("0x%08X", ea),
				Name:        name,
				Partition:   part,
				Description: desc,
				DataType:    dtype,
				TotalAccess: total,
				Reads:       reads,
				Writes:      writes,
			}
			list = append(list, sym)
		}
	}
	return list, nil
}

// GetDBCuratedRAMSymbols returns curated RAM symbols from curated_ram, ram_variables, or symbols table if present
func (s *DBStore) GetDBCuratedRAMSymbols() ([]RAMSymbol, bool) {
	if s.hasTable("curated_ram") {
		cols := s.tableColumns("curated_ram")
		eaCol := pickCol(cols, "ea", "address", "addr")
		if eaCol != "" {
			query := fmt.Sprintf(`
				SELECT %s, %s, %s, %s, %s, %s, %s
				FROM curated_ram
				ORDER BY %s
			`,
				eaCol,
				colStr(cols, "", "RAM_VAR", "name", "symbol"),
				colStr(cols, "", "RAM", "partition", "partition_name", "subsystem"),
				colStr(cols, "", "1.0", "scaling", "category", "factor"),
				colStr(cols, "", "uint16", "data_type", "type"),
				colStr(cols, "", "", "unit", "units"),
				colStr(cols, "", "", "description", "comment", "notes"),
				eaCol,
			)
			if rows, err := s.conn().Query(query); err == nil {
				defer rows.Close()
				var list []RAMSymbol
				for rows.Next() {
					var sym RAMSymbol
					var rawEA interface{}
					if err := rows.Scan(&rawEA, &sym.Name, &sym.Partition, &sym.Scaling, &sym.DataType, &sym.Unit, &sym.Description); err == nil {
						if v, ok := parseFlexInt64(rawEA); ok {
							sym.EA = uint32(v)
						}
						sym.EAHex = fmt.Sprintf("0x%08X", sym.EA)
						list = append(list, sym)
					}
				}
				if len(list) > 0 {
					return list, true
				}
			}
		}
	}

	// Fallback for custom DBs that store RAM entries in ram_variables or symbols
	if !strings.Contains(strings.ToLower(s.getDBPath()), "flash_analysis.db") {
		if s.hasTable("ram_variables") {
			cols := s.tableColumns("ram_variables")
			addrCol := pickCol(cols, "address", "ea")
			if addrCol != "" {
				query := fmt.Sprintf(`
					SELECT %s, %s, %s, %s, %s, %s, %s
					FROM ram_variables
					LIMIT 250
				`,
					addrCol,
					colStr(cols, "", "RAM_VAR", "name", "symbol"),
					colStr(cols, "", "RAM", "subsystem", "partition"),
					colStr(cols, "", "1.0", "scaling"),
					colStr(cols, "", "uint16", "data_type"),
					colStr(cols, "", "", "units", "unit"),
					colStr(cols, "", "", "description", "notes"),
				)
				if rows, err := s.conn().Query(query); err == nil {
					defer rows.Close()
					var list []RAMSymbol
					for rows.Next() {
						var sym RAMSymbol
						var rawEA interface{}
						if err := rows.Scan(&rawEA, &sym.Name, &sym.Partition, &sym.Scaling, &sym.DataType, &sym.Unit, &sym.Description); err == nil {
							if v, ok := parseFlexInt64(rawEA); ok {
								sym.EA = uint32(v)
							}
							sym.EAHex = fmt.Sprintf("0x%08X", sym.EA)
							list = append(list, sym)
						}
					}
					if len(list) > 0 {
						return list, true
					}
				}
			}
		}

		if s.hasTable("symbols") {
			cols := s.tableColumns("symbols")
			if cols["ea"] && cols["name"] {
				query := fmt.Sprintf(`
					SELECT ea, %s, %s, %s, %s
					FROM symbols
					WHERE UPPER(COALESCE(category, '')) LIKE '%%RAM%%' OR UPPER(COALESCE(partition_name, '')) LIKE '%%RAM%%' OR UPPER(COALESCE(partition_name, '')) LIKE '%%SRAM%%'
					ORDER BY ea
					LIMIT 250
				`,
					colStr(cols, "", "SYM", "name"),
					colStr(cols, "", "RAM", "partition_name"),
					colStr(cols, "", "", "category"),
					colStr(cols, "", "", "description"),
				)
				if rows, err := s.conn().Query(query); err == nil {
					defer rows.Close()
					var list []RAMSymbol
					for rows.Next() {
						var sym RAMSymbol
						var rawEA interface{}
						if err := rows.Scan(&rawEA, &sym.Name, &sym.Partition, &sym.Scaling, &sym.Description); err == nil {
							if v, ok := parseFlexInt64(rawEA); ok {
								sym.EA = uint32(v)
							}
							sym.EAHex = fmt.Sprintf("0x%08X", sym.EA)
							sym.DataType = "symbol"
							list = append(list, sym)
						}
					}
					if len(list) > 0 {
						return list, true
					}
				}
			}
		}
	}

	return nil, false
}

// GetDBUDSDIDs returns UDS DID and DTC records from the SQLite uds_dids table if present
func (s *DBStore) GetDBUDSDIDs() ([]UDSDid, bool) {
	if !s.hasTable("uds_dids") {
		return nil, false
	}
	cols := s.tableColumns("uds_dids")
	didCol := pickCol(cols, "did", "did_hex")
	if didCol == "" {
		return nil, false
	}

	var isAWD int
	if s.hasTable("partitions") {
		_ = s.conn().QueryRow("SELECT COUNT(*) FROM partitions WHERE name = 'CAL_DPP1'").Scan(&isAWD)
	}

	query := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s, %s, %s, %s, %s, %s
		FROM uds_dids
		ORDER BY %s
	`,
		colStr(cols, "", "", "did", "did_hex"),
		colStr(cols, "", "", "name"),
		colStr(cols, "", "Read (0x22)", "access", "category"),
		colStr(cols, "", "Default (0x01) / Extended (0x03)", "sessions", "permitted_sessions_read"),
		colStr(cols, "", "", "security", "security_access_required"),
		colStr(cols, "", "", "memory_target", "ram_ea", "target_address"),
		colInt(cols, "", 0, "length_bytes", "data_length_bytes"),
		colStr(cols, "", "", "description", "notes"),
		colStr(cols, "", "", "handler_ea", "table_entry_address"),
		colStr(cols, "", "", "handler_name", "handler_fn"),
		didCol,
	)

	rows, err := s.conn().Query(query)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	hasFns := s.hasTable("functions") && s.tableColumns("functions")["name"]
	var list []UDSDid
	for rows.Next() {
		var d UDSDid
		if err := rows.Scan(
			&d.DID, &d.Name, &d.Access, &d.Sessions, &d.Security,
			&d.MemoryTarget, &d.LengthBytes, &d.Description, &d.HandlerEA, &d.HandlerName,
		); err == nil {
			if d.Security == "" {
				if strings.Contains(strings.ToLower(d.Access), "write") {
					d.Security = "Security Access Required (Write)"
				} else {
					d.Security = "None (0x22 Read / 0x19 DTC)"
				}
			}
			if d.HandlerEA == "" {
				if strings.Contains(d.HandlerName, "0x") || strings.Contains(d.HandlerName, "0X") {
					d.HandlerEA = d.HandlerName
				} else if hasFns && d.HandlerName != "" {
					var fnEA uint32
					if err := s.conn().QueryRow("SELECT ea FROM functions WHERE name = ? LIMIT 1", d.HandlerName).Scan(&fnEA); err == nil && fnEA > 0 {
						d.HandlerEA = fmt.Sprintf("0x%08X", fnEA)
					}
				}
				if d.HandlerEA == "" && isAWD > 0 {
					d.HandlerEA = "0x00C291E4"
				}
			}
			list = append(list, d)
		}
	}
	if len(list) == 0 {
		return nil, false
	}
	return list, true
}

func (s *DBStore) buildFunctionSelectCols(cols map[string]bool) string {
	eaExpr := pickCol(cols, "ea", "start_ea", "address")
	if eaExpr == "" {
		eaExpr = "0"
	}
	endExpr := colInt(cols, "", 0, "end_ea")
	nameExpr := colStr(cols, "", "sub_unknown", "name", "fn_name", "symbol")
	partExpr := colStr(cols, "", "FLASH", "partition_name", "partition")
	sizeExpr := colInt(cols, "", 0, "size_bytes", "size")
	insnExpr := colInt(cols, "", 0, "insn_count")
	retExpr := colInt(cols, "", 1, "has_return")
	callerExpr := colInt(cols, "", 0, "caller_count")
	calleeExpr := colInt(cols, "", 0, "callee_count")
	memReadExpr := colInt(cols, "", 0, "mem_read_count")
	memWriteExpr := colInt(cols, "", 0, "mem_write_count")

	return fmt.Sprintf(
		"%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s",
		eaExpr, nameExpr, partExpr, endExpr, sizeExpr, insnExpr,
		retExpr, callerExpr, calleeExpr, memReadExpr, memWriteExpr,
	)
}

func scanFunctionRow(scanner interface {
	Scan(dest ...interface{}) error
}) (Function, error) {
	var f Function
	var rawEA, rawEnd interface{}
	var hasRetInt int
	err := scanner.Scan(
		&rawEA, &f.Name, &f.PartitionName, &rawEnd, &f.SizeBytes, &f.InsnCount,
		&hasRetInt, &f.CallerCount, &f.CalleeCount, &f.MemReadCount, &f.MemWriteCount,
	)
	if err != nil {
		return f, err
	}
	if v, ok := parseFlexInt64(rawEA); ok {
		f.EA = uint32(v)
	}
	if v, ok := parseFlexInt64(rawEnd); ok {
		f.EndEA = uint32(v)
	}
	if f.EndEA == 0 && f.SizeBytes > 0 {
		f.EndEA = f.EA + uint32(f.SizeBytes)
	}
	f.HasReturn = hasRetInt == 1
	f.EAHex = fmt.Sprintf("0x%08X", f.EA)
	f.EndEAHex = fmt.Sprintf("0x%08X", f.EndEA)
	return f, nil
}

// GetFunctionByEA retrieves a function by exact address or enclosing range
func (s *DBStore) GetFunctionByEA(ea uint32) (*Function, error) {
	if !s.hasTable("functions") {
		return nil, sql.ErrNoRows
	}
	cols := s.tableColumns("functions")
	eaCol := pickCol(cols, "ea", "start_ea", "address")
	if eaCol == "" {
		return nil, sql.ErrNoRows
	}
	selCols := s.buildFunctionSelectCols(cols)

	row := s.conn().QueryRow(fmt.Sprintf("SELECT %s FROM functions WHERE %s = ?", selCols, eaCol), ea)
	f, err := scanFunctionRow(row)
	if err == sql.ErrNoRows && cols["end_ea"] {
		row = s.conn().QueryRow(fmt.Sprintf("SELECT %s FROM functions WHERE %s <= ? AND end_ea > ? LIMIT 1", selCols, eaCol), ea, ea)
		f, err = scanFunctionRow(row)
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// SearchFunctions queries functions by name or address
func (s *DBStore) SearchFunctions(query string, partition string, limit, offset int) ([]Function, int, error) {
	if !s.hasTable("functions") {
		return []Function{}, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	cols := s.tableColumns("functions")
	eaCol := pickCol(cols, "ea", "start_ea", "address")
	nameCol := pickCol(cols, "name", "fn_name", "symbol")
	partCol := pickCol(cols, "partition_name", "partition")

	whereParts := []string{"1=1"}
	var args []interface{}

	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil && eaCol != "" {
				if cols["end_ea"] {
					whereParts = append(whereParts, fmt.Sprintf("(%s = ? OR (%s <= ? AND end_ea > ?))", eaCol, eaCol))
					args = append(args, val, val, val)
				} else {
					whereParts = append(whereParts, fmt.Sprintf("%s = ?", eaCol))
					args = append(args, val)
				}
			}
		} else if nameCol != "" {
			whereParts = append(whereParts, fmt.Sprintf("%s LIKE ?", nameCol))
			args = append(args, "%"+q+"%")
		}
	}

	if partition != "" && partition != "ALL" && partCol != "" {
		whereParts = append(whereParts, fmt.Sprintf("%s = ?", partCol))
		args = append(args, partition)
	}

	whereClause := strings.Join(whereParts, " AND ")
	total := s.safeCount(fmt.Sprintf("SELECT COUNT(*) FROM functions WHERE %s", whereClause), args...)

	orderCol := "1"
	if eaCol != "" {
		orderCol = eaCol
	}

	querySQL := fmt.Sprintf(`
		SELECT %s
		FROM functions
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, s.buildFunctionSelectCols(cols), whereClause, orderCol)

	rows, err := s.conn().Query(querySQL, append(args, limit, offset)...)
	if err != nil {
		return []Function{}, 0, nil
	}
	defer rows.Close()

	list := make([]Function, 0)
	for rows.Next() {
		if f, err := scanFunctionRow(rows); err == nil {
			list = append(list, f)
		}
	}

	return list, total, nil
}

// GetFunctionInstructions gets all decoded instructions for a function
func (s *DBStore) GetFunctionInstructions(functionEA uint32) ([]Instruction, error) {
	if !s.hasTable("instructions") {
		return []Instruction{}, nil
	}
	cols := s.tableColumns("instructions")
	fnCol := pickCol(cols, "function_ea", "fn_ea")
	eaCol := pickCol(cols, "ea", "insn_ea", "address")
	if fnCol == "" || eaCol == "" {
		return []Instruction{}, nil
	}

	query := fmt.Sprintf(`
		SELECT %s, %s, %s, %s, %s, %s, %s, %s
		FROM instructions
		WHERE %s = ?
		ORDER BY %s
	`,
		eaCol,
		fnCol,
		colStr(cols, "", "", "partition_name", "partition"),
		colStr(cols, "", "", "mnemonic"),
		colStr(cols, "", "", "op_str", "operands"),
		colInt(cols, "", 2, "size_bytes", "size"),
		colInt(cols, "", 0, "is_call"),
		colStr(cols, "", "", "call_type"),
		fnCol,
		eaCol,
	)

	rows, err := s.conn().Query(query, functionEA)
	if err != nil {
		return []Instruction{}, nil
	}
	defer rows.Close()

	insns := make([]Instruction, 0)
	for rows.Next() {
		var i Instruction
		var rawEA, rawFnEA interface{}
		var isCallInt int
		if err := rows.Scan(&rawEA, &rawFnEA, &i.PartitionName, &i.Mnemonic, &i.OpStr, &i.SizeBytes, &isCallInt, &i.CallType); err == nil {
			if v, ok := parseFlexInt64(rawEA); ok {
				i.EA = uint32(v)
			}
			if v, ok := parseFlexInt64(rawFnEA); ok {
				i.FunctionEA = uint32(v)
			}
			i.EAHex = fmt.Sprintf("0x%08X", i.EA)
			mnLower := strings.ToLower(strings.TrimSpace(i.Mnemonic))
			if isCallInt == 1 {
				i.IsCall = true
				if i.CallType == "" {
					i.CallType = "DIRECT"
				}
			} else if mnLower == "call" || mnLower == "fcall" || mnLower == "bl" || mnLower == "jarl" || mnLower == "bsr" || mnLower == "jsr" || mnLower == "calls" || mnLower == "calla" || mnLower == "callr" || mnLower == "e_bl" {
				i.IsCall = true
				i.CallType = "DIRECT"
			} else if mnLower == "calli" || mnLower == "fcalli" || mnLower == "blx" || mnLower == "calli.a" {
				i.IsCall = true
				i.CallType = "INDIRECT"
			}
			insns = append(insns, i)
		}
	}
	return insns, nil
}

// GetFunctionCallers gets all callers to a function
func (s *DBStore) GetFunctionCallers(calleeFn uint32) ([]CallEdge, error) {
	if !s.hasTable("calls") {
		return []CallEdge{}, nil
	}
	cols := s.tableColumns("calls")
	hasFns := s.hasTable("functions")

	calleeFnExpr := "c.callee_ea"
	whereCond := "c.callee_ea = ?"
	args := []interface{}{calleeFn}
	if cols["callee_fn"] {
		calleeFnExpr = "COALESCE(c.callee_fn, c.callee_ea)"
		whereCond = "(c.callee_fn = ? OR c.callee_ea = ?)"
		args = []interface{}{calleeFn, calleeFn}
	}

	fnJoin := ""
	callerNameExpr := "''"
	if hasFns {
		fnJoin = "LEFT JOIN functions f ON c.caller_fn = f.ea"
		callerNameExpr = "COALESCE(f.name, '')"
	}

	query := fmt.Sprintf(`
		SELECT COALESCE(c.caller_ea, 0), COALESCE(c.caller_fn, 0), %s, %s,
		       COALESCE(c.callee_ea, 0), %s, %s, %s, %s
		FROM calls c
		%s
		WHERE %s
		ORDER BY c.caller_ea
		LIMIT 100
	`,
		callerNameExpr,
		colStr(cols, "c", "", "caller_partition"),
		calleeFnExpr,
		colStr(cols, "c", "", "callee_partition"),
		colStr(cols, "c", "DIRECT", "call_type"),
		colInt(cols, "c", 0, "is_cross_partition"),
		fnJoin,
		whereCond,
	)

	rows, err := s.conn().Query(query, args...)
	if err != nil {
		return []CallEdge{}, nil
	}
	defer rows.Close()

	list := make([]CallEdge, 0)
	for rows.Next() {
		var ce CallEdge
		var isCross int
		if err := rows.Scan(
			&ce.CallerEA, &ce.CallerFn, &ce.CallerFnName, &ce.CallerPartition,
			&ce.CalleeEA, &ce.CalleeFn, &ce.CalleePartition, &ce.CallType, &isCross,
		); err == nil {
			ce.CallerEAHex = fmt.Sprintf("0x%08X", ce.CallerEA)
			ce.CallerFnHex = fmt.Sprintf("0x%08X", ce.CallerFn)
			ce.CalleeEAHex = fmt.Sprintf("0x%08X", ce.CalleeEA)
			ce.CalleeFnHex = fmt.Sprintf("0x%08X", ce.CalleeFn)
			ce.IsCross = isCross == 1
			list = append(list, ce)
		}
	}
	return list, nil
}

// GetFunctionCallees gets all functions called by a source function
func (s *DBStore) GetFunctionCallees(callerFn uint32) ([]CallEdge, error) {
	if !s.hasTable("calls") {
		return []CallEdge{}, nil
	}
	cols := s.tableColumns("calls")
	hasFns := s.hasTable("functions")

	calleeFnExpr := "COALESCE(c.callee_ea, 0)"
	if cols["callee_fn"] {
		calleeFnExpr = "COALESCE(c.callee_fn, c.callee_ea)"
	}

	fnJoin := ""
	calleeNameExpr := "''"
	if cols["callee_fn_name"] && hasFns {
		fnJoin = fmt.Sprintf("LEFT JOIN functions f ON %s = f.ea", calleeFnExpr)
		calleeNameExpr = "COALESCE(NULLIF(c.callee_fn_name, ''), COALESCE(f.name, ''))"
	} else if cols["callee_fn_name"] {
		calleeNameExpr = "COALESCE(c.callee_fn_name, '')"
	} else if hasFns {
		fnJoin = fmt.Sprintf("LEFT JOIN functions f ON %s = f.ea", calleeFnExpr)
		calleeNameExpr = "COALESCE(f.name, '')"
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT COALESCE(c.caller_ea, 0), COALESCE(c.caller_fn, 0), %s,
		                COALESCE(c.callee_ea, 0), %s, %s, %s,
		                %s, %s
		FROM calls c
		%s
		WHERE c.caller_fn = ?
		ORDER BY c.caller_ea
		LIMIT 100
	`,
		colStr(cols, "c", "", "caller_partition"),
		calleeFnExpr,
		calleeNameExpr,
		colStr(cols, "c", "", "callee_partition"),
		colStr(cols, "c", "DIRECT", "call_type"),
		colInt(cols, "c", 0, "is_cross_partition"),
		fnJoin,
	)

	rows, err := s.conn().Query(query, callerFn)
	if err != nil {
		return []CallEdge{}, nil
	}
	defer rows.Close()

	list := make([]CallEdge, 0)
	for rows.Next() {
		var ce CallEdge
		var isCross int
		if err := rows.Scan(
			&ce.CallerEA, &ce.CallerFn, &ce.CallerPartition,
			&ce.CalleeEA, &ce.CalleeFn, &ce.CalleeFnName, &ce.CalleePartition,
			&ce.CallType, &isCross,
		); err == nil {
			ce.CallerEAHex = fmt.Sprintf("0x%08X", ce.CallerEA)
			ce.CallerFnHex = fmt.Sprintf("0x%08X", ce.CallerFn)
			ce.CalleeEAHex = fmt.Sprintf("0x%08X", ce.CalleeEA)
			ce.CalleeFnHex = fmt.Sprintf("0x%08X", ce.CalleeFn)
			ce.IsCross = isCross == 1
			list = append(list, ce)
		}
	}
	return list, nil
}

// GetFunctionMemoryAccess gets memory interactions for a function
func (s *DBStore) GetFunctionMemoryAccess(functionEA uint32) ([]MemoryInteraction, error) {
	if !s.hasTable("memory_interactions") {
		return []MemoryInteraction{}, nil
	}
	cols := s.tableColumns("memory_interactions")
	query := fmt.Sprintf(`
		SELECT %s, %s, %s, '', %s,
		       %s, %s, %s, %s,
		       %s, %s, %s, %s
		FROM memory_interactions m
		WHERE m.function_ea = ?
		ORDER BY m.insn_ea
		LIMIT 150
	`,
		colInt(cols, "m", 0, "id", "rowid"),
		colInt(cols, "m", 0, "insn_ea"),
		colInt(cols, "m", 0, "function_ea"),
		colStr(cols, "m", "", "source_partition"),
		colInt(cols, "m", 0, "target_ea"),
		colStr(cols, "m", "", "target_partition"),
		colStr(cols, "m", "READ", "access_type"),
		colInt(cols, "m", 4, "access_width"),
		colStr(cols, "m", "", "target_symbol"),
		colStr(cols, "m", "", "mnemonic"),
		colStr(cols, "m", "", "op_str"),
		colInt(cols, "m", 0, "is_cross_partition"),
	)

	rows, err := s.conn().Query(query, functionEA)
	if err != nil {
		return []MemoryInteraction{}, nil
	}
	defer rows.Close()

	list := make([]MemoryInteraction, 0)
	for rows.Next() {
		var mi MemoryInteraction
		var isCross int
		if err := rows.Scan(
			&mi.ID, &mi.InsnEA, &mi.FunctionEA, &mi.FunctionName, &mi.SourcePartition,
			&mi.TargetEA, &mi.TargetPartition, &mi.AccessType, &mi.AccessWidth,
			&mi.TargetSymbol, &mi.Mnemonic, &mi.OpStr, &isCross,
		); err == nil {
			mi.InsnEAHex = fmt.Sprintf("0x%08X", mi.InsnEA)
			mi.FunctionEAHex = fmt.Sprintf("0x%08X", mi.FunctionEA)
			mi.TargetEAHex = fmt.Sprintf("0x%08X", mi.TargetEA)
			mi.IsCross = isCross == 1
			list = append(list, mi)
		}
	}
	return list, nil
}

// DescribeSQLiteDB inspects any SQLite .db file briefly to build a human-readable category summary
func DescribeSQLiteDB(dbPath string) string {
	lower := strings.ToLower(dbPath)
	prefix := "Custom ECU Database"
	if strings.Contains(lower, "mdps") || strings.Contains(lower, "eps") {
		prefix = "MDPS Power Steering ECU"
	} else if strings.Contains(lower, "adas") {
		prefix = "ADAS / Radar / Camera ECU"
	} else if strings.Contains(lower, "abs") || strings.Contains(lower, "esc") {
		prefix = "ABS / ESC Chassis ECU"
	} else if strings.Contains(lower, "tcu") {
		prefix = "Transmission Control Unit (TCU)"
	} else if strings.Contains(lower, "awd") {
		prefix = "AWD Coupling Controller"
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return prefix
	}
	defer db.Close()

	var mapsCnt, fnCnt, partCnt int
	_ = db.QueryRow("SELECT COUNT(*) FROM cal_maps").Scan(&mapsCnt)
	_ = db.QueryRow("SELECT COUNT(*) FROM functions").Scan(&fnCnt)
	_ = db.QueryRow("SELECT COUNT(*) FROM partitions").Scan(&partCnt)

	var details []string
	if mapsCnt > 0 {
		details = append(details, fmt.Sprintf("%d maps", mapsCnt))
	}
	if fnCnt > 0 {
		details = append(details, fmt.Sprintf("%d fns", fnCnt))
	}
	if partCnt > 0 && len(details) < 2 {
		details = append(details, fmt.Sprintf("%d partitions", partCnt))
	}
	if len(details) > 0 {
		return fmt.Sprintf("%s (%s)", prefix, strings.Join(details, ", "))
	}
	return prefix
}
