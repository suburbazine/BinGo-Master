package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// DBStore wraps SQLite queries for SIM2K-260 analysis
type DBStore struct {
	db *sql.DB
}

// NewDBStore opens connection to flash_analysis.db or initializes an empty schema if none exists
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

	// Verify connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return &DBStore{db: db}, nil
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
		has_return INTEGER NOT NULL
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
		is_call INTEGER NOT NULL,
		call_type TEXT
	);
	CREATE TABLE IF NOT EXISTS calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		caller_ea INTEGER NOT NULL,
		caller_fn INTEGER NOT NULL,
		caller_partition TEXT NOT NULL,
		callee_ea INTEGER NOT NULL,
		callee_fn_name TEXT NOT NULL,
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
		applicability TEXT DEFAULT 'UNKNOWN',
		applicability_note TEXT DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS dflash_blocks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sector INTEGER NOT NULL,
		offset INTEGER NOT NULL,
		block_id INTEGER NOT NULL,
		generation INTEGER NOT NULL,
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

// Close closes the database connection
func (s *DBStore) Close() error {
	return s.db.Close()
}

// SwitchDB closes the current SQLite database and opens a new one at dbPath
func (s *DBStore) SwitchDB(dbPath string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database file not found: %w", err)
	}
	newDB, err := sql.Open("sqlite3", dbPath+"?cache=shared")
	if err != nil {
		return fmt.Errorf("failed to open sqlite database '%s': %w", dbPath, err)
	}
	if err := newDB.Ping(); err != nil {
		newDB.Close()
		return fmt.Errorf("failed to connect to database '%s': %w", dbPath, err)
	}
	oldDB := s.db
	s.db = newDB
	if oldDB != nil {
		_ = oldDB.Close()
	}
	return nil
}

// GetStats returns summary counts

func (s *DBStore) GetStats() (Stats, error) {
	var st Stats
	row := s.db.QueryRow(`
		SELECT 
			(SELECT COUNT(*) FROM instructions),
			(SELECT COUNT(*) FROM functions),
			(SELECT COUNT(*) FROM calls),
			(SELECT COUNT(*) FROM memory_interactions),
			(SELECT COUNT(*) FROM cal_maps),
			(SELECT COUNT(*) FROM dflash_blocks),
			(SELECT COUNT(*) FROM symbols),
			(SELECT COUNT(*) FROM partitions),
			(SELECT COUNT(*) FROM cal_maps WHERE applicability = 'LIVE_ACTUATOR'),
			(SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('DEAD_INACTIVE', 'IDENTITY')),
			(SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('TORQUE_MODEL_REF', 'AXIS')),
			(SELECT COUNT(*) FROM cal_maps WHERE applicability IN ('GATED_DISABLED', 'DIAGNOSTIC'))
	`)
	err := row.Scan(
		&st.InstructionCount,
		&st.FunctionCount,
		&st.CallCount,
		&st.MemoryInteractions,
		&st.MapCount,
		&st.DFlashBlockCount,
		&st.SymbolCount,
		&st.PartitionCount,
		&st.LiveMapCount,
		&st.DeadMapCount,
		&st.TorqueMapCount,
		&st.GatedMapCount,
	)
	return st, err
}

// GetPartitions returns all flash partitions with instruction and function counts
func (s *DBStore) GetPartitions() ([]Partition, error) {
	rows, err := s.db.Query(`
		SELECT 
			p.id, p.name, p.start_ea, p.end_ea, p.file_start, p.file_end, p.memory_type, p.description,
			(SELECT COUNT(*) FROM functions f WHERE f.partition_name = p.name) as fn_count,
			(SELECT COUNT(*) FROM instructions i WHERE i.partition_name = p.name) as insn_count
		FROM partitions p
		ORDER BY p.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Partition
	for rows.Next() {
		var p Partition
		err := rows.Scan(
			&p.ID, &p.Name, &p.StartEA, &p.EndEA, &p.FileStart, &p.FileEnd,
			&p.MemoryType, &p.Description, &p.FnCount, &p.InsnCount,
		)
		if err != nil {
			return nil, err
		}
		p.StartEAHex = fmt.Sprintf("0x%08X", p.StartEA)
		p.EndEAHex = fmt.Sprintf("0x%08X", p.EndEA)
		if p.EndEA >= p.StartEA {
			p.SizeKB = int((p.EndEA - p.StartEA + 1) / 1024)
		}
		list = append(list, p)
	}
	return list, nil
}

// GetMatrix returns cross-partition interaction counts
func (s *DBStore) GetMatrix() ([]MatrixCell, error) {
	rows, err := s.db.Query(`
		SELECT source_partition, target_partition, COUNT(*) as cnt
		FROM memory_interactions
		WHERE is_cross_partition = 1
		GROUP BY source_partition, target_partition
		ORDER BY cnt DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cells []MatrixCell
	for rows.Next() {
		var c MatrixCell
		if err := rows.Scan(&c.SourcePartition, &c.TargetPartition, &c.Count); err != nil {
			return nil, err
		}
		cells = append(cells, c)
	}
	return cells, nil
}

// GetMatrixInteractions returns boundary traffic between two partitions
func (s *DBStore) GetMatrixInteractions(src, dst, accType string, limit, offset int) ([]MemoryInteraction, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	whereClause := "WHERE m.is_cross_partition = 1"
	var args []interface{}

	if src != "" && src != "ALL" {
		whereClause += " AND m.source_partition = ?"
		args = append(args, src)
	}
	if dst != "" && dst != "ALL" {
		whereClause += " AND m.target_partition = ?"
		args = append(args, dst)
	}
	if accType != "" && accType != "ALL" {
		whereClause += " AND m.access_type = ?"
		args = append(args, accType)
	}

	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM memory_interactions m %s", whereClause)
	var total int
	err := s.db.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT m.id, m.insn_ea, m.function_ea, COALESCE(f.name, ''), m.source_partition,
		       m.target_ea, m.target_partition, m.access_type, m.access_width,
		       COALESCE(m.target_symbol, ''), m.mnemonic, m.op_str, m.is_cross_partition
		FROM memory_interactions m
		LEFT JOIN functions f ON m.function_ea = f.ea
		%s
		ORDER BY m.insn_ea
		LIMIT ? OFFSET ?
	`, whereClause)

	queryArgs := append(args, limit, offset)
	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []MemoryInteraction
	for rows.Next() {
		var mi MemoryInteraction
		var isCrossInt int
		err := rows.Scan(
			&mi.ID, &mi.InsnEA, &mi.FunctionEA, &mi.FunctionName, &mi.SourcePartition,
			&mi.TargetEA, &mi.TargetPartition, &mi.AccessType, &mi.AccessWidth,
			&mi.TargetSymbol, &mi.Mnemonic, &mi.OpStr, &isCrossInt,
		)
		if err != nil {
			return nil, 0, err
		}
		mi.InsnEAHex = fmt.Sprintf("0x%08X", mi.InsnEA)
		mi.FunctionEAHex = fmt.Sprintf("0x%08X", mi.FunctionEA)
		mi.TargetEAHex = fmt.Sprintf("0x%08X", mi.TargetEA)
		mi.IsCross = isCrossInt == 1
		list = append(list, mi)
	}
	return list, total, nil
}

// SearchMaps queries calibration maps by name, folder, file offset, EA, or applicability
func (s *DBStore) SearchMaps(query, folder, mapType, applicability string, limit, offset int) ([]CalMap, int, error) {
	if limit <= 0 {
		limit = 250
	}
	if limit > 5000 {
		limit = 5000
	}

	whereParts := []string{"1=1"}
	var args []interface{}

	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil {
				whereParts = append(whereParts, "(ea = ? OR file_offset = ?)")
				args = append(args, val, val)
			}
		} else if strings.HasPrefix(q, "$") {
			if val, err := strconv.ParseUint(q[1:], 16, 64); err == nil {
				whereParts = append(whereParts, "(file_offset = ? OR ea = ?)")
				args = append(args, val, 0x80800000+(val-0x200000))
			}
		} else {
			whereParts = append(whereParts, "(name LIKE ? OR folder LIKE ? OR comment LIKE ?)")
			pattern := "%" + q + "%"
			args = append(args, pattern, pattern, pattern)
		}
	}

	if folder != "" && folder != "ALL" {
		whereParts = append(whereParts, "folder LIKE ?")
		args = append(args, "%"+folder+"%")
	}

	if mapType != "" && mapType != "ALL" {
		switch mapType {
		case "eZweidim":
			whereParts = append(whereParts, "map_type IN ('eZweidim', '2D_MAP')")
		case "eEindim":
			whereParts = append(whereParts, "map_type IN ('eEindim', '1D_CURVE', 'AXIS')")
		case "eEinzel":
			whereParts = append(whereParts, "map_type IN ('eEinzel', 'SCALAR', 'ID_STRING')")
		default:
			whereParts = append(whereParts, "map_type = ?")
			args = append(args, mapType)
		}
	}

	if applicability != "" && applicability != "ALL" {
		switch applicability {
		case "DEAD_INACTIVE":
			whereParts = append(whereParts, "applicability IN ('DEAD_INACTIVE', 'IDENTITY')")
		case "TORQUE_MODEL_REF":
			whereParts = append(whereParts, "applicability IN ('TORQUE_MODEL_REF', 'AXIS')")
		case "GATED_DISABLED":
			whereParts = append(whereParts, "applicability IN ('GATED_DISABLED', 'DIAGNOSTIC')")
		default:
			whereParts = append(whereParts, "applicability = ?")
			args = append(args, applicability)
		}
	}

	whereClause := strings.Join(whereParts, " AND ")
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM cal_maps WHERE %s", whereClause)
	var total int
	err := s.db.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	sqlQuery := fmt.Sprintf(`
		SELECT id, name, file_offset, ea, folder, map_type, columns, rows,
		       data_org, unit, factor, offset, axis_x_ea, COALESCE(axis_x_name, ''),
		       axis_y_ea, COALESCE(axis_y_name, ''), COALESCE(comment, ''),
		       ref_count, COALESCE(ref_functions, ''),
		       COALESCE(applicability, 'LIVE_ACTUATOR'), COALESCE(applicability_note, '')
		FROM cal_maps
		WHERE %s
		ORDER BY
			CASE COALESCE(applicability, 'LIVE_ACTUATOR')
				WHEN 'LIVE_ACTUATOR' THEN 0
				WHEN 'GATED_DISABLED' THEN 1
				WHEN 'DIAGNOSTIC' THEN 2
				WHEN 'DEAD_INACTIVE' THEN 3
				WHEN 'IDENTITY' THEN 4
				ELSE 5
			END,
			ea ASC
		LIMIT ? OFFSET ?
	`, whereClause)

	queryArgs := append(args, limit, offset)
	rows, err := s.db.Query(sqlQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var maps []CalMap
	for rows.Next() {
		var m CalMap
		err := rows.Scan(
			&m.ID, &m.Name, &m.FileOffset, &m.EA, &m.Folder, &m.MapType,
			&m.Columns, &m.Rows, &m.DataOrg, &m.Unit, &m.Factor, &m.Offset,
			&m.AxisXEA, &m.AxisXName, &m.AxisYEA, &m.AxisYName, &m.Comment,
			&m.RefCount, &m.RefFunctions,
			&m.Applicability, &m.ApplicabilityNote,
		)
		if err != nil {
			return nil, 0, err
		}
		m.FileOffsetHex = fmt.Sprintf("$%06X", m.FileOffset)
		m.EAHex = fmt.Sprintf("0x%08X", m.EA)
		if m.AxisXEA != nil {
			m.AxisXEAHex = fmt.Sprintf("0x%08X", *m.AxisXEA)
		}
		if m.AxisYEA != nil {
			m.AxisYEAHex = fmt.Sprintf("0x%08X", *m.AxisYEA)
		}
		if m.RefFunctions != "" {
			parts := strings.Split(m.RefFunctions, ",")
			for _, p := range parts {
				m.RefFnList = append(m.RefFnList, strings.TrimSpace(p))
			}
		}
		maps = append(maps, m)
	}

	return maps, total, nil
}

// GetMapByID retrieves a single calibration map
func (s *DBStore) GetMapByID(id int) (*CalMap, error) {
	row := s.db.QueryRow(`
		SELECT id, name, file_offset, ea, folder, map_type, columns, rows,
		       data_org, unit, factor, offset, axis_x_ea, COALESCE(axis_x_name, ''),
		       axis_y_ea, COALESCE(axis_y_name, ''), COALESCE(comment, ''),
		       ref_count, COALESCE(ref_functions, ''),
		       COALESCE(applicability, 'LIVE_ACTUATOR'), COALESCE(applicability_note, '')
		FROM cal_maps
		WHERE id = ?
	`, id)

	var m CalMap
	err := row.Scan(
		&m.ID, &m.Name, &m.FileOffset, &m.EA, &m.Folder, &m.MapType,
		&m.Columns, &m.Rows, &m.DataOrg, &m.Unit, &m.Factor, &m.Offset,
		&m.AxisXEA, &m.AxisXName, &m.AxisYEA, &m.AxisYName, &m.Comment,
		&m.RefCount, &m.RefFunctions,
		&m.Applicability, &m.ApplicabilityNote,
	)
	if err != nil {
		return nil, err
	}
	m.FileOffsetHex = fmt.Sprintf("$%06X", m.FileOffset)
	m.EAHex = fmt.Sprintf("0x%08X", m.EA)
	if m.AxisXEA != nil {
		m.AxisXEAHex = fmt.Sprintf("0x%08X", *m.AxisXEA)
	}
	if m.AxisYEA != nil {
		m.AxisYEAHex = fmt.Sprintf("0x%08X", *m.AxisYEA)
	}
	if m.RefFunctions != "" {
		parts := strings.Split(m.RefFunctions, ",")
		for _, p := range parts {
			m.RefFnList = append(m.RefFnList, strings.TrimSpace(p))
		}
	}
	return &m, nil
}

// GetDFlashSectors returns sector summary
func (s *DBStore) GetDFlashSectors() []DFlashSector {
	var s0Count, s0Active, s1Count, s1Active int
	s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(is_active), 0) FROM dflash_blocks WHERE sector_idx = 0").Scan(&s0Count, &s0Active)
	s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(is_active), 0) FROM dflash_blocks WHERE sector_idx = 1").Scan(&s1Count, &s1Active)

	var isAWD int
	s.db.QueryRow("SELECT COUNT(*) FROM partitions WHERE name = 'CAL_DPP1'").Scan(&isAWD)
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

// GetDFlashBlocks queries blocks inventory
func (s *DBStore) GetDFlashBlocks(sector *int, activeOnly bool, category string) ([]DFlashBlock, error) {
	whereParts := []string{"1=1"}
	var args []interface{}

	if sector != nil {
		whereParts = append(whereParts, "sector_idx = ?")
		args = append(args, *sector)
	}
	if activeOnly {
		whereParts = append(whereParts, "is_active = 1")
	}
	if category != "" && category != "ALL" {
		whereParts = append(whereParts, "category = ?")
		args = append(args, category)
	}

	query := fmt.Sprintf(`
		SELECT id, sector_idx, block_id, offset, dflash_ea, version_count,
		       is_active, length, crc, name, category, COALESCE(description, ''),
		       payload_hex, payload_ascii
		FROM dflash_blocks
		WHERE %s
		ORDER BY block_id, sector_idx DESC, version_count DESC
	`, strings.Join(whereParts, " AND "))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blocks []DFlashBlock
	for rows.Next() {
		var b DFlashBlock
		var isActiveInt int
		err := rows.Scan(
			&b.ID, &b.SectorIdx, &b.BlockID, &b.Offset, &b.DFlashEA, &b.VersionCount,
			&isActiveInt, &b.Length, &b.CRC, &b.Name, &b.Category, &b.Description,
			&b.PayloadHex, &b.PayloadASCII,
		)
		if err != nil {
			return nil, err
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

// GetDFlashBlockDetail returns block detail and all historical revisions
func (s *DBStore) GetDFlashBlockDetail(blockID int) (*DFlashBlockDetail, error) {
	rows, err := s.db.Query(`
		SELECT id, sector_idx, block_id, offset, dflash_ea, version_count,
		       is_active, length, crc, name, category, COALESCE(description, ''),
		       payload_hex, payload_ascii
		FROM dflash_blocks
		WHERE block_id = ?
		ORDER BY sector_idx DESC, version_count DESC, offset DESC
	`, blockID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []DFlashBlock
	for rows.Next() {
		var b DFlashBlock
		var isActiveInt int
		err := rows.Scan(
			&b.ID, &b.SectorIdx, &b.BlockID, &b.Offset, &b.DFlashEA, &b.VersionCount,
			&isActiveInt, &b.Length, &b.CRC, &b.Name, &b.Category, &b.Description,
			&b.PayloadHex, &b.PayloadASCII,
		)
		if err != nil {
			return nil, err
		}
		b.IsActive = isActiveInt == 1
		b.BlockIDHex = fmt.Sprintf("0x%02X", b.BlockID)
		b.OffsetHex = fmt.Sprintf("0x%05X", b.Offset)
		b.DFlashEAHex = fmt.Sprintf("0x%08X", b.DFlashEA)
		b.CRCHex = fmt.Sprintf("0x%04X", b.CRC)
		list = append(list, b)
	}

	if len(list) == 0 {
		return nil, fmt.Errorf("block 0x%02X not found", blockID)
	}

	// Active block is preferably active in Sector 1
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
	// Producers (writes / modifies)
	prodRows, err := s.db.Query(`
		SELECT DISTINCT m.function_ea, COALESCE(f.name, ''), m.source_partition,
		                m.insn_ea, m.access_type, m.mnemonic, m.op_str
		FROM memory_interactions m
		LEFT JOIN functions f ON m.function_ea = f.ea
		WHERE m.target_ea = ? AND m.access_type IN ('WRITE', 'MODIFY')
		ORDER BY m.insn_ea
		LIMIT 50
	`, targetEA)
	if err != nil {
		return 0, 0, nil, nil, err
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

	// Consumers (reads / addr loads)
	consRows, err := s.db.Query(`
		SELECT DISTINCT m.function_ea, COALESCE(f.name, ''), m.source_partition,
		                m.insn_ea, m.access_type, m.mnemonic, m.op_str
		FROM memory_interactions m
		LEFT JOIN functions f ON m.function_ea = f.ea
		WHERE m.target_ea = ? AND m.access_type IN ('READ', 'ADDR_LOAD')
		ORDER BY m.insn_ea
		LIMIT 50
	`, targetEA)
	if err != nil {
		return 0, 0, nil, nil, err
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

	var reads, writes int
	s.db.QueryRow("SELECT COUNT(*) FROM memory_interactions WHERE target_ea = ? AND access_type IN ('READ', 'ADDR_LOAD')", targetEA).Scan(&reads)
	s.db.QueryRow("SELECT COUNT(*) FROM memory_interactions WHERE target_ea = ? AND access_type IN ('WRITE', 'MODIFY')", targetEA).Scan(&writes)

	return reads, writes, producers, consumers, nil
}

// SearchRAMAddresses finds top accessed RAM addresses matching query
func (s *DBStore) SearchRAMAddresses(query string, limit int) ([]RAMSymbol, error) {
	if limit <= 0 {
		limit = 50
	}

	whereClause := "WHERE target_partition IN ('DSPR', 'LMU', 'RAM_DPP3', 'RAM_DPP0')"
	var args []interface{}
	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil {
				whereClause += " AND target_ea = ?"
				args = append(args, val)
			}
		} else {
			whereClause += " AND (target_symbol LIKE ? OR op_str LIKE ?)"
			args = append(args, "%"+q+"%", "%"+q+"%")
		}
	}

	querySQL := fmt.Sprintf(`
		SELECT target_ea, target_partition, COUNT(*) as cnt,
		       SUM(CASE WHEN access_type IN ('READ', 'ADDR_LOAD') THEN 1 ELSE 0 END) as r_cnt,
		       SUM(CASE WHEN access_type IN ('WRITE', 'MODIFY') THEN 1 ELSE 0 END) as w_cnt
		FROM memory_interactions
		%s
		GROUP BY target_ea
		ORDER BY cnt DESC
		LIMIT ?
	`, whereClause)

	args = append(args, limit)
	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []RAMSymbol
	for rows.Next() {
		var ea uint32
		var part string
		var total, reads, writes int
		if err := rows.Scan(&ea, &part, &total, &reads, &writes); err == nil {
			name := fmt.Sprintf("RAM_%08X", ea)
			desc := fmt.Sprintf("Active RAM variable at 0x%08X (%d reads, %d writes)", ea, reads, writes)
			dtype := "uint32"
			_ = s.db.QueryRow("SELECT name, data_type, description FROM curated_ram WHERE ea = ?", ea).Scan(&name, &dtype, &desc)
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

// GetDBCuratedRAMSymbols returns curated RAM symbols from the SQLite curated_ram table if present
func (s *DBStore) GetDBCuratedRAMSymbols() ([]RAMSymbol, bool) {
	rows, err := s.db.Query(`
		SELECT ea, name, partition, category, data_type, unit, description
		FROM curated_ram
		ORDER BY ea
	`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var list []RAMSymbol
	for rows.Next() {
		var sym RAMSymbol
		var cat string
		if err := rows.Scan(
			&sym.EA, &sym.Name, &sym.Partition, &cat,
			&sym.DataType, &sym.Unit, &sym.Description,
		); err == nil {
			sym.EAHex = fmt.Sprintf("0x%08X", sym.EA)
			sym.Scaling = cat
			list = append(list, sym)
		}
	}
	if len(list) == 0 {
		return nil, false
	}
	return list, true
}

// GetDBUDSDIDs returns UDS DID and DTC records from the SQLite uds_dids table if present
func (s *DBStore) GetDBUDSDIDs() ([]UDSDid, bool) {
	rows, err := s.db.Query(`
		SELECT did, name, category, ram_ea, length_bytes, description, handler_fn
		FROM uds_dids
		ORDER BY did
	`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var list []UDSDid
	for rows.Next() {
		var d UDSDid
		if err := rows.Scan(
			&d.DID, &d.Name, &d.Access, &d.MemoryTarget,
			&d.LengthBytes, &d.Description, &d.HandlerName,
		); err == nil {
			d.Sessions = "Default (0x01) / Extended (0x03)"
			if strings.Contains(d.Access, "Write") {
				d.Security = "Level 0x11 Required (Write)"
			} else {
				d.Security = "None (0x22 Read / 0x19 DTC)"
			}
			d.HandlerEA = "0x00C291E4"
			list = append(list, d)
		}
	}
	if len(list) == 0 {
		return nil, false
	}
	return list, true
}

// GetFunctionByEA retrieves a function by exact address or enclosing range
func (s *DBStore) GetFunctionByEA(ea uint32) (*Function, error) {
	row := s.db.QueryRow(`
		SELECT ea, name, partition_name, end_ea, size_bytes, insn_count,
		       has_return, caller_count, callee_count, mem_read_count, mem_write_count
		FROM functions
		WHERE ea = ?
	`, ea)

	var f Function
	var hasRetInt int
	err := row.Scan(
		&f.EA, &f.Name, &f.PartitionName, &f.EndEA, &f.SizeBytes, &f.InsnCount,
		&hasRetInt, &f.CallerCount, &f.CalleeCount, &f.MemReadCount, &f.MemWriteCount,
	)
	if err == sql.ErrNoRows {
		// Try enclosing range
		row = s.db.QueryRow(`
			SELECT ea, name, partition_name, end_ea, size_bytes, insn_count,
			       has_return, caller_count, callee_count, mem_read_count, mem_write_count
			FROM functions
			WHERE ea <= ? AND end_ea > ?
			LIMIT 1
		`, ea, ea)
		err = row.Scan(
			&f.EA, &f.Name, &f.PartitionName, &f.EndEA, &f.SizeBytes, &f.InsnCount,
			&hasRetInt, &f.CallerCount, &f.CalleeCount, &f.MemReadCount, &f.MemWriteCount,
		)
	}
	if err != nil {
		return nil, err
	}

	f.HasReturn = hasRetInt == 1
	f.EAHex = fmt.Sprintf("0x%08X", f.EA)
	f.EndEAHex = fmt.Sprintf("0x%08X", f.EndEA)
	return &f, nil
}

// SearchFunctions queries functions by name or address
func (s *DBStore) SearchFunctions(query string, partition string, limit, offset int) ([]Function, int, error) {
	if limit <= 0 {
		limit = 50
	}
	whereParts := []string{"1=1"}
	var args []interface{}

	if query != "" {
		q := strings.TrimSpace(query)
		if strings.HasPrefix(q, "0x") || strings.HasPrefix(q, "0X") {
			if val, err := strconv.ParseUint(q[2:], 16, 64); err == nil {
				whereParts = append(whereParts, "(ea = ? OR (ea <= ? AND end_ea > ?))")
				args = append(args, val, val, val)
			}
		} else {
			whereParts = append(whereParts, "name LIKE ?")
			args = append(args, "%"+q+"%")
		}
	}

	if partition != "" && partition != "ALL" {
		whereParts = append(whereParts, "partition_name = ?")
		args = append(args, partition)
	}

	whereClause := strings.Join(whereParts, " AND ")
	var total int
	s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM functions WHERE %s", whereClause), args...).Scan(&total)

	querySQL := fmt.Sprintf(`
		SELECT ea, name, partition_name, end_ea, size_bytes, insn_count,
		       has_return, caller_count, callee_count, mem_read_count, mem_write_count
		FROM functions
		WHERE %s
		ORDER BY ea
		LIMIT ? OFFSET ?
	`, whereClause)

	rows, err := s.db.Query(querySQL, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var list []Function
	for rows.Next() {
		var f Function
		var hasRetInt int
		err := rows.Scan(
			&f.EA, &f.Name, &f.PartitionName, &f.EndEA, &f.SizeBytes, &f.InsnCount,
			&hasRetInt, &f.CallerCount, &f.CalleeCount, &f.MemReadCount, &f.MemWriteCount,
		)
		if err == nil {
			f.HasReturn = hasRetInt == 1
			f.EAHex = fmt.Sprintf("0x%08X", f.EA)
			f.EndEAHex = fmt.Sprintf("0x%08X", f.EndEA)
			list = append(list, f)
		}
	}

	return list, total, nil
}

// GetFunctionInstructions gets all decoded instructions for a function
func (s *DBStore) GetFunctionInstructions(functionEA uint32) ([]Instruction, error) {
	rows, err := s.db.Query(`
		SELECT ea, function_ea, partition_name, mnemonic, op_str, size_bytes
		FROM instructions
		WHERE function_ea = ?
		ORDER BY ea
	`, functionEA)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var insns []Instruction
	for rows.Next() {
		var i Instruction
		if err := rows.Scan(&i.EA, &i.FunctionEA, &i.PartitionName, &i.Mnemonic, &i.OpStr, &i.SizeBytes); err == nil {
			i.EAHex = fmt.Sprintf("0x%08X", i.EA)
			if i.Mnemonic == "call" || i.Mnemonic == "fcall" {
				i.IsCall = true
				i.CallType = "DIRECT"
			} else if i.Mnemonic == "calli" || i.Mnemonic == "fcalli" {
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
	rows, err := s.db.Query(`
		SELECT c.caller_ea, c.caller_fn, COALESCE(f.name, ''), c.caller_partition,
		       c.callee_ea, c.callee_fn, c.callee_partition, c.call_type, c.is_cross_partition
		FROM calls c
		LEFT JOIN functions f ON c.caller_fn = f.ea
		WHERE c.callee_fn = ? OR c.callee_ea = ?
		ORDER BY c.caller_ea
		LIMIT 100
	`, calleeFn, calleeFn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CallEdge
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
	rows, err := s.db.Query(`
		SELECT DISTINCT c.caller_ea, c.caller_fn, c.caller_partition,
		                c.callee_ea, c.callee_fn, COALESCE(f.name, ''), c.callee_partition,
		                c.call_type, c.is_cross_partition
		FROM calls c
		LEFT JOIN functions f ON c.callee_fn = f.ea
		WHERE c.caller_fn = ?
		ORDER BY c.caller_ea
		LIMIT 100
	`, callerFn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []CallEdge
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
	rows, err := s.db.Query(`
		SELECT m.id, m.insn_ea, m.function_ea, '', m.source_partition,
		       m.target_ea, m.target_partition, m.access_type, m.access_width,
		       COALESCE(m.target_symbol, ''), m.mnemonic, m.op_str, m.is_cross_partition
		FROM memory_interactions m
		WHERE m.function_ea = ?
		ORDER BY m.insn_ea
		LIMIT 150
	`, functionEA)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []MemoryInteraction
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
