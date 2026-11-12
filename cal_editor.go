package main

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// tcuImageBase detects a Kefico CPTSH 8AT TCU calibration binary (magic 0x87654383 BE)
// and returns (baseEA, headerOffset, isTCU).
// - 785,408 B (0xBFC00) OBD read starts at 0x001C0200 (magic at offset 0x000)
// - 786,432 B (0xC0000) full-sector image starts at 0x001C0000 (magic at offset 0x200)
func tcuImageBase(data []byte) (uint32, int, bool) {
	if len(data) >= 0x80 && binary.BigEndian.Uint32(data[0:4]) == 0x87654383 {
		return 0x001C0200, 0, true
	}
	if len(data) >= 0x280 && binary.BigEndian.Uint32(data[0x200:0x204]) == 0x87654383 {
		return 0x001C0000, 0x200, true
	}
	return 0, 0, false
}

var (
	crcTable   [256]uint32
	crcTableMu sync.Once
)

func initCRCTable() {
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if (c & 0x80000000) != 0 {
				c = ((c << 1) ^ 0x04C11DB7) & 0xFFFFFFFF
			} else {
				c = (c << 1) & 0xFFFFFFFF
			}
		}
		crcTable[i] = c
	}
}

// CRC32MSB calculates the SIM2K-260 calibration CRC-32 (MSB-first, poly 0x04C11DB7, init 0, no final XOR)
func CRC32MSB(buf []byte) uint32 {
	crcTableMu.Do(initCRCTable)
	var c uint32 = 0
	for _, b := range buf {
		idx := ((c >> 24) ^ uint32(b)) & 0xFF
		c = (crcTable[idx] ^ (c << 8)) & 0xFFFFFFFF
	}
	return c
}

// BinManager holds the in-memory binary image, active DB memory map context, and session modifications
type BinManager struct {
	mu            sync.RWMutex
	binPath       string
	dbPath        string
	partitions    []Partition
	axisMaps      map[uint32]CalMap
	extraScanDirs []string
	extraBinPaths []string
	originalData  []byte
	currentData   []byte
	loaded        bool
	diffs         map[string]DiffRecord // key: "mapID:row:col"
}

// NewBinManager initializes the binary manager
func NewBinManager(binPath string) *BinManager {
	bm := &BinManager{
		binPath:  binPath,
		axisMaps: make(map[uint32]CalMap),
		diffs:    make(map[string]DiffRecord),
	}
	if binPath != "" {
		if err := bm.Load(binPath); err != nil {
			fmt.Printf("[BinManager] Warning: could not load initial bin '%s': %v\n", binPath, err)
		}
	}
	return bm
}

// SetDBContext updates the active database path, partition layout, and axis map metadata
func (bm *BinManager) SetDBContext(dbPath string, partitions []Partition, axisMaps map[uint32]CalMap) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.dbPath = dbPath
	bm.partitions = partitions
	if axisMaps != nil {
		bm.axisMaps = axisMaps
	} else {
		bm.axisMaps = make(map[uint32]CalMap)
	}
}

// SetExtraSources registers custom directories and explicit binary paths for discovery
func (bm *BinManager) SetExtraSources(scanDirs []string, binPaths []string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.extraScanDirs = append([]string(nil), scanDirs...)
	bm.extraBinPaths = append([]string(nil), binPaths...)
}

// Unload clears any currently loaded binary (used when switching to a custom DB with no matching bin yet)
func (bm *BinManager) Unload() {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.binPath = ""
	bm.originalData = nil
	bm.currentData = nil
	bm.loaded = false
	bm.diffs = make(map[string]DiffRecord)
}

// Load loads a calibration/firmware binary file of any ECU family into memory
func (bm *BinManager) Load(path string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 32 {
		return fmt.Errorf("invalid binary size: %d bytes (must be at least 32 bytes)", len(data))
	}

	bm.binPath = filepath.ToSlash(path)
	bm.originalData = make([]byte, len(data))
	copy(bm.originalData, data)
	bm.currentData = make([]byte, len(data))
	copy(bm.currentData, data)
	bm.diffs = make(map[string]DiffRecord)
	bm.loaded = true

	fmt.Printf("[BinManager] Successfully loaded calibration binary (%d bytes): %s\n", len(data), path)
	return nil
}

// IsLoaded returns whether a binary is currently loaded
func (bm *BinManager) IsLoaded() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.loaded
}

// GetBinPath returns the currently loaded binary path
func (bm *BinManager) GetBinPath() string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.binPath
}

// GetLoadedSize returns the byte length of the currently loaded binary
func (bm *BinManager) GetLoadedSize() int {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return len(bm.currentData)
}

// normalizeDataOrg maps both WinOLS and standard C/Python/Rust data type strings to canonical organizations
func normalizeDataOrg(org string) string {
	clean := strings.TrimSpace(org)
	switch clean {
	case "eFloatLoHi", "eFloatHiLo", "eLongLoHi", "eLongHiLo", "eSLongLoHi", "eSLongHiLo",
		"eLoHi", "eSLoHi", "eHiLo", "eSHiLo", "eSByte", "eByte":
		return clean
	}

	switch strings.ToLower(clean) {
	case "float32", "float", "f32", "float32_le", "f32_le", "efloatlohi":
		return "eFloatLoHi"
	case "float32_be", "f32_be", "float_be", "efloathilo":
		return "eFloatHiLo"
	case "uint32", "u32", "ulong", "uint32_le", "u32_le", "dword", "elonglohi":
		return "eLongLoHi"
	case "uint32_be", "u32_be", "ulong_be", "elonghilo":
		return "eLongHiLo"
	case "int32", "i32", "s32", "slong", "int32_le", "i32_le", "s32_le", "eslonglohi":
		return "eSLongLoHi"
	case "int32_be", "i32_be", "s32_be", "slong_be", "eslonghilo":
		return "eSLongHiLo"
	case "uint16", "u16", "uword", "word", "uint16_le", "u16_le", "elohi":
		return "eLoHi"
	case "uint16_be", "u16_be", "uword_be", "word_be", "ehilo":
		return "eHiLo"
	case "int16", "i16", "s16", "sword", "short", "int16_le", "i16_le", "s16_le", "eslohi":
		return "eSLoHi"
	case "int16_be", "i16_be", "s16_be", "sword_be", "short_be", "eshilo":
		return "eSHiLo"
	case "int8", "i8", "s8", "sbyte", "char", "esbyte":
		return "eSByte"
	default:
		return "eByte"
	}
}

func isBigEndianOrg(org string) bool {
	switch normalizeDataOrg(org) {
	case "eFloatHiLo", "eLongHiLo", "eSLongHiLo", "eHiLo", "eSHiLo":
		return true
	default:
		return false
	}
}

func bytesPerCellForOrg(org string) int {
	switch normalizeDataOrg(org) {
	case "eFloatLoHi", "eFloatHiLo", "eLongLoHi", "eLongHiLo", "eSLongLoHi", "eSLongHiLo":
		return 4
	case "eLoHi", "eSLoHi", "eHiLo", "eSHiLo":
		return 2
	default:
		return 1
	}
}

func (bm *BinManager) isAWDDBLocked() bool {
	if strings.Contains(strings.ToLower(filepath.Base(bm.dbPath)), "awd_") {
		return true
	}
	for _, p := range bm.partitions {
		if p.Name == "CAL_DPP1" {
			return true
		}
	}
	return false
}

func (bm *BinManager) isSIM2KDBLocked() bool {
	base := strings.ToLower(filepath.Base(bm.dbPath))
	return base == "flash_analysis.db"
}

func (bm *BinManager) resolveMapOffset(m CalMap) int {
	n := len(bm.currentData)
	if n == 0 {
		return m.FileOffset
	}

	// 1. Kefico CPTSH 8AT TCU image (785,408 B at 0x1C0200 or 786,432 B at 0x1C0000)
	if base, _, ok := tcuImageBase(bm.currentData); ok && m.EA >= base && m.EA < 0x00280000 {
		return int(m.EA - base)
	}

	// 2. AWD ECU partial/combined dumps when AWD DB is active
	if bm.isAWDDBLocked() {
		if n == 0x8000 && m.EA >= 0x00C04000 && m.EA < 0x00C0C000 {
			return int(m.EA - 0x00C04000)
		}
		if n == 0xC000 && m.EA >= 0x00C00000 && m.EA < 0x00C0C000 {
			return int(m.EA - 0x00C00000)
		}
		if n == 0x4000 && m.EA >= 0x00C00000 && m.EA < 0x00C04000 {
			return int(m.EA - 0x00C00000)
		}
	}

	// 3. Partition-aware resolution for custom DBs (handles both full flash dumps and calibration-only slice bins)
	if m.EA > 0 && len(bm.partitions) > 0 {
		for _, p := range bm.partitions {
			if m.EA >= p.StartEA && m.EA <= p.EndEA && p.EndEA >= p.StartEA {
				partLen := int(uint64(p.EndEA) - uint64(p.StartEA) + 1)
				// If loaded binary matches this single partition's exact byte size (e.g. cal-only .bin slice)
				if partLen == n {
					return int(m.EA - p.StartEA)
				}
				if p.FileStart != nil && p.FileEnd != nil && (*p.FileEnd-*p.FileStart+1) == n {
					return int(m.EA - p.StartEA)
				}
			}
		}

		// If FileOffset is out of range for the loaded bin or unset (0 while EA > 0 and partition maps elsewhere)
		if m.FileOffset < 0 || m.FileOffset >= n || (m.FileOffset == 0 && m.EA > 0) {
			for _, p := range bm.partitions {
				if m.EA >= p.StartEA && m.EA <= p.EndEA {
					if p.FileStart != nil {
						cand := *p.FileStart + int(m.EA-p.StartEA)
						if cand >= 0 && cand < n {
							return cand
						}
					}
					// Also support cal-slice binary starting at p.StartEA
					sliceCand := int(m.EA - p.StartEA)
					if m.FileOffset >= n && sliceCand >= 0 && sliceCand < n {
						return sliceCand
					}
				}
			}
		}
	}

	return m.FileOffset
}

func decodeCellValue(buf []byte, org string, factor, offset float64) (int64, float64) {
	if factor == 0 {
		factor = 1.0
	}
	switch normalizeDataOrg(org) {
	case "eFloatLoHi":
		bits := binary.LittleEndian.Uint32(buf[:4])
		f := float64(math.Float32frombits(bits))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		return int64(bits), f*factor + offset
	case "eFloatHiLo":
		bits := binary.BigEndian.Uint32(buf[:4])
		f := float64(math.Float32frombits(bits))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		return int64(bits), f*factor + offset
	case "eLongLoHi":
		raw := int64(binary.LittleEndian.Uint32(buf[:4]))
		return raw, float64(raw)*factor + offset
	case "eLongHiLo":
		raw := int64(binary.BigEndian.Uint32(buf[:4]))
		return raw, float64(raw)*factor + offset
	case "eSLongLoHi":
		raw := int64(int32(binary.LittleEndian.Uint32(buf[:4])))
		return raw, float64(raw)*factor + offset
	case "eSLongHiLo":
		raw := int64(int32(binary.BigEndian.Uint32(buf[:4])))
		return raw, float64(raw)*factor + offset
	case "eHiLo":
		raw := int64(binary.BigEndian.Uint16(buf[:2]))
		return raw, float64(raw)*factor + offset
	case "eSHiLo":
		raw := int64(int16(binary.BigEndian.Uint16(buf[:2])))
		return raw, float64(raw)*factor + offset
	case "eSLoHi":
		raw := int64(int16(binary.LittleEndian.Uint16(buf[:2])))
		return raw, float64(raw)*factor + offset
	case "eLoHi":
		raw := int64(binary.LittleEndian.Uint16(buf[:2]))
		return raw, float64(raw)*factor + offset
	case "eSByte":
		raw := int64(int8(buf[0]))
		return raw, float64(raw)*factor + offset
	default:
		raw := int64(buf[0])
		return raw, float64(raw)*factor + offset
	}
}

// ReadMapData reads raw and physical cell values for a given map
func (bm *BinManager) ReadMapData(m CalMap) (MapDataResponse, error) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	resp := MapDataResponse{
		Map:       m,
		XAxis:     make([]AxisPoint, 0),
		YAxis:     make([]AxisPoint, 0),
		Cells:     make([][]CellPoint, 0),
		IsEdited:  false,
		BinLoaded: bm.loaded,
	}

	if !bm.loaded {
		return resp, fmt.Errorf("no calibration binary (.bin) loaded yet")
	}

	cols := m.Columns
	if cols < 1 {
		cols = 1
	}
	rows := m.Rows
	if rows < 1 {
		rows = 1
	}

	bytesPerCell := bytesPerCellForOrg(m.DataOrg)
	baseOffset := bm.resolveMapOffset(m)

	totalBytes := cols * rows * bytesPerCell
	if baseOffset < 0 || baseOffset+totalBytes > len(bm.currentData) {
		return resp, fmt.Errorf("map offset 0x%X (EA 0x%X, %d bytes) is outside loaded binary '%s' (%d bytes)", baseOffset, m.EA, totalBytes, filepath.Base(bm.binPath), len(bm.currentData))
	}

	// Read Cells
	resp.Cells = make([][]CellPoint, rows)
	for r := 0; r < rows; r++ {
		resp.Cells[r] = make([]CellPoint, cols)
		for c := 0; c < cols; c++ {
			cellOffset := baseOffset + (r*cols+c)*bytesPerCell
			raw, physical := decodeCellValue(bm.currentData[cellOffset:cellOffset+bytesPerCell], m.DataOrg, m.Factor, m.Offset)
			origRaw, _ := decodeCellValue(bm.originalData[cellOffset:cellOffset+bytesPerCell], m.DataOrg, m.Factor, m.Offset)

			key := fmt.Sprintf("%d:%d:%d", m.ID, r, c)
			isMod := false
			if _, exists := bm.diffs[key]; exists {
				isMod = true
				resp.IsEdited = true
			}

			resp.Cells[r][c] = CellPoint{
				Row:         r,
				Col:         c,
				FileOffset:  cellOffset,
				Raw:         raw,
				Physical:    math.Round(physical*10000) / 10000,
				OriginalRaw: origRaw,
				IsModified:  isMod,
			}
		}
	}

	// Read X-Axis & Y-Axis
	resp.XAxis = bm.readAxis(m.AxisXEA, cols, m.AxisXName, m, baseOffset)
	resp.YAxis = bm.readAxis(m.AxisYEA, rows, m.AxisYName, m, baseOffset)

	return resp, nil
}

func formatAxisLabel(phys float64, unit string) string {
	rounded := math.Round(phys*1000) / 1000
	var s string
	if math.Abs(rounded-math.Round(rounded)) < 1e-6 {
		s = fmt.Sprintf("%.0f", rounded)
	} else {
		s = strconvFormatFloatClean(rounded)
	}
	if unit != "" {
		return s + " " + unit
	}
	return s
}

func strconvFormatFloatClean(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

func (bm *BinManager) resolveEAOffsetLocked(ea uint32, parentMap CalMap, parentBaseOffset int) int {
	n := len(bm.currentData)
	if n == 0 {
		return -1
	}

	// SIM2K-260 calibration partition
	if bm.isSIM2KDBLocked() && ea >= 0x80800000 && ea < 0x80A00000 {
		return int(ea - 0x80800000 + 0x200000)
	}

	// AWD C166 calibration/RAM partitions
	if bm.isAWDDBLocked() && ea >= 0x00C00000 && ea < 0x00C0C000 {
		if n == 0x8000 && ea >= 0x00C04000 {
			return int(ea - 0x00C04000)
		}
		return int(ea - 0x00C00000)
	}

	// Partition table lookup for custom DBs (MDPS, etc.)
	for _, p := range bm.partitions {
		if ea >= p.StartEA && ea <= p.EndEA && p.EndEA >= p.StartEA {
			partLen := int(uint64(p.EndEA) - uint64(p.StartEA) + 1)
			if partLen == n {
				return int(ea - p.StartEA)
			}
			if p.FileStart != nil {
				cand := *p.FileStart + int(ea-p.StartEA)
				if cand >= 0 && cand < n {
					return cand
				}
			}
		}
	}

	// Relative EA offset from parent map (works even if partitions table is absent)
	if parentMap.EA > 0 && parentBaseOffset >= 0 {
		rel := int64(parentBaseOffset) + (int64(ea) - int64(parentMap.EA))
		if rel >= 0 && rel < int64(n) {
			return int(rel)
		}
	}

	// Direct 0-based flat binary offset
	if int64(ea) >= 0 && int64(ea) < int64(n) {
		return int(ea)
	}

	return -1
}

func (bm *BinManager) readAxis(axisEA *uint32, count int, name string, parentMap CalMap, parentBaseOffset int) []AxisPoint {
	axis := make([]AxisPoint, count)
	defaultAxis := func() []AxisPoint {
		for i := 0; i < count; i++ {
			axis[i] = AxisPoint{
				Index:    i,
				Raw:      i,
				Physical: float64(i),
				Label:    fmt.Sprintf("[%d]", i),
			}
		}
		return axis
	}
	if axisEA == nil || count <= 0 {
		return defaultAxis()
	}

	ea := *axisEA
	n := len(bm.currentData)

	// 1. Kefico CPTSH 8AT TCU Big-Endian calibration partition (0x001C0000..0x0027FFFF)
	if base, _, ok := tcuImageBase(bm.currentData); ok && ea >= base && ea < 0x00280000 {
		axOffset := int(ea - base)
		if bytesPerCellForOrg(parentMap.DataOrg) == 1 && axOffset+count <= n {
			shiftLabels14 := []string{"1->2", "2->3", "3->4", "4->5", "5->6", "6->7", "7->8", "2->1", "3->2", "4->3", "5->4", "6->5", "7->6", "8->7"}
			downshiftLabels7 := []string{"2->1", "3->2", "4->3", "5->4", "6->5", "7->6", "8->7"}
			for i := 0; i < count; i++ {
				val := int(bm.currentData[axOffset+i])
				lbl := fmt.Sprintf("%d", val)
				phys := float64(val)
				if count == 14 && strings.Contains(name, "Shift") && i < len(shiftLabels14) {
					lbl = fmt.Sprintf("%s (%d)", shiftLabels14[i], val)
				} else if count == 7 && strings.Contains(name, "Downshift") && i < len(downshiftLabels7) {
					lbl = fmt.Sprintf("%s (%d)", downshiftLabels7[i], val)
				} else if strings.Contains(name, "Decel") {
					phys = float64(val-128) / 100.0
					lbl = fmt.Sprintf("%.2fg (%d)", phys, val)
				}
				axis[i] = AxisPoint{
					Index:    i,
					Raw:      val,
					Physical: phys,
					Label:    lbl,
				}
			}
			return axis
		} else if axOffset+count*2 <= n {
			shiftLabels14 := []string{"1->2", "2->3", "3->4", "4->5", "5->6", "6->7", "7->8", "2->1", "3->2", "4->3", "5->4", "6->5", "7->6", "8->7"}
			for i := 0; i < count; i++ {
				val := int(binary.BigEndian.Uint16(bm.currentData[axOffset+i*2 : axOffset+i*2+2]))
				lbl := fmt.Sprintf("%d", val)
				phys := float64(val)
				if count == 14 && strings.Contains(name, "Shift") && i < len(shiftLabels14) {
					lbl = fmt.Sprintf("%s (%d)", shiftLabels14[i], val)
				} else if strings.Contains(name, "APS") || strings.Contains(name, "Throttle") {
					phys = float64(val) * 100.0 / 16384.0
					lbl = fmt.Sprintf("%.1f%% (%d)", phys, val)
				}
				axis[i] = AxisPoint{
					Index:    i,
					Raw:      val,
					Physical: phys,
					Label:    lbl,
				}
			}
			return axis
		}
		return defaultAxis()
	}

	// 2. If the axis EA itself is defined as an entry in cal_maps, use its exact DataOrg, Factor, Offset, and Offset resolution!
	if axMap, exists := bm.axisMaps[ea]; exists && !bm.isSIM2KDBLocked() {
		axOff := bm.resolveMapOffset(axMap)
		bpc := bytesPerCellForOrg(axMap.DataOrg)
		if axOff >= 0 && axOff+count*bpc <= n {
			for i := 0; i < count; i++ {
				raw, phys := decodeCellValue(bm.currentData[axOff+i*bpc:axOff+(i+1)*bpc], axMap.DataOrg, axMap.Factor, axMap.Offset)
				axis[i] = AxisPoint{
					Index:    i,
					Raw:      int(raw),
					Physical: math.Round(phys*1000) / 1000,
					Label:    formatAxisLabel(phys, axMap.Unit),
				}
			}
			return axis
		}
	}

	// 3. Generic EA -> file offset resolution across SIM2K, AWD, and custom DBs
	axOffset := bm.resolveEAOffsetLocked(ea, parentMap, parentBaseOffset)
	if axOffset < 0 {
		return defaultAxis()
	}

	bigEndian := isBigEndianOrg(parentMap.DataOrg)
	if axOffset+count*2 <= n {
		for i := 0; i < count; i++ {
			var val int
			if bigEndian {
				val = int(binary.BigEndian.Uint16(bm.currentData[axOffset+i*2 : axOffset+i*2+2]))
			} else {
				val = int(binary.LittleEndian.Uint16(bm.currentData[axOffset+i*2 : axOffset+i*2+2]))
			}
			axis[i] = AxisPoint{
				Index:    i,
				Raw:      val,
				Physical: float64(val),
				Label:    fmt.Sprintf("%d", val),
			}
		}
	} else if axOffset+count <= n {
		for i := 0; i < count; i++ {
			val := int(bm.currentData[axOffset+i])
			axis[i] = AxisPoint{
				Index:    i,
				Raw:      val,
				Physical: float64(val),
				Label:    fmt.Sprintf("%d", val),
			}
		}
	} else {
		return defaultAxis()
	}
	return axis
}

// ApplyCellEdits updates cell values in memory and logs diffs
func (bm *BinManager) ApplyCellEdits(m CalMap, edits []CellEdit) ([]DiffRecord, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if !bm.loaded {
		return nil, fmt.Errorf("no binary loaded")
	}

	cols := m.Columns
	if cols < 1 {
		cols = 1
	}
	normOrg := normalizeDataOrg(m.DataOrg)
	bytesPerCell := bytesPerCellForOrg(normOrg)
	baseOffset := bm.resolveMapOffset(m)

	factor := m.Factor
	if factor == 0 {
		factor = 1.0
	}

	var applied []DiffRecord

	for _, edit := range edits {
		if edit.Row < 0 || edit.Row >= m.Rows || edit.Col < 0 || edit.Col >= m.Columns {
			continue
		}

		cellOffset := baseOffset + (edit.Row*cols+edit.Col)*bytesPerCell
		if cellOffset < 0 || cellOffset+bytesPerCell > len(bm.currentData) {
			continue
		}

		oldRaw, oldPhys := decodeCellValue(bm.currentData[cellOffset:cellOffset+bytesPerCell], normOrg, factor, m.Offset)

		var newRaw int64
		var newPhys float64

		if normOrg == "eFloatLoHi" || normOrg == "eFloatHiLo" {
			var bits uint32
			if edit.Physical != nil {
				newPhys = *edit.Physical
				f32 := float32((newPhys - m.Offset) / factor)
				bits = math.Float32bits(f32)
				newRaw = int64(bits)
			} else if edit.Raw != nil {
				bits = uint32(*edit.Raw)
				newRaw = int64(bits)
				newPhys = float64(math.Float32frombits(bits))*factor + m.Offset
			} else {
				continue
			}
			if normOrg == "eFloatHiLo" {
				binary.BigEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], bits)
			} else {
				binary.LittleEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], bits)
			}
		} else {
			if edit.Raw != nil {
				newRaw = *edit.Raw
				newPhys = float64(newRaw)*factor + m.Offset
			} else if edit.Physical != nil {
				newPhys = *edit.Physical
				newRaw = int64(math.Round((newPhys - m.Offset) / factor))
			} else {
				continue
			}

			switch bytesPerCell {
			case 1:
				if normOrg == "eSByte" {
					if newRaw < -128 {
						newRaw = -128
					}
					if newRaw > 127 {
						newRaw = 127
					}
					bm.currentData[cellOffset] = byte(int8(newRaw))
				} else {
					if newRaw < 0 {
						newRaw = 0
					}
					if newRaw > 255 {
						newRaw = 255
					}
					bm.currentData[cellOffset] = byte(newRaw)
				}
			case 2:
				if normOrg == "eSLoHi" || normOrg == "eSHiLo" {
					if newRaw < -32768 {
						newRaw = -32768
					}
					if newRaw > 32767 {
						newRaw = 32767
					}
					if normOrg == "eSHiLo" {
						binary.BigEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(int16(newRaw)))
					} else {
						binary.LittleEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(int16(newRaw)))
					}
				} else {
					if newRaw < 0 {
						newRaw = 0
					}
					if newRaw > 65535 {
						newRaw = 65535
					}
					if normOrg == "eHiLo" {
						binary.BigEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(newRaw))
					} else {
						binary.LittleEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(newRaw))
					}
				}
			case 4:
				if normOrg == "eSLongLoHi" || normOrg == "eSLongHiLo" {
					if newRaw < math.MinInt32 {
						newRaw = math.MinInt32
					}
					if newRaw > math.MaxInt32 {
						newRaw = math.MaxInt32
					}
					if normOrg == "eSLongHiLo" {
						binary.BigEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(int32(newRaw)))
					} else {
						binary.LittleEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(int32(newRaw)))
					}
				} else {
					if newRaw < 0 {
						newRaw = 0
					}
					if newRaw > math.MaxUint32 {
						newRaw = math.MaxUint32
					}
					if normOrg == "eLongHiLo" {
						binary.BigEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(newRaw))
					} else {
						binary.LittleEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(newRaw))
					}
				}
			}
		}

		key := fmt.Sprintf("%d:%d:%d", m.ID, edit.Row, edit.Col)
		rec := DiffRecord{
			MapID:       m.ID,
			MapName:     m.Name,
			FileOffset:  cellOffset,
			Row:         edit.Row,
			Col:         edit.Col,
			OldRaw:      oldRaw,
			NewRaw:      newRaw,
			OldPhysical: math.Round(oldPhys*10000) / 10000,
			NewPhysical: math.Round(newPhys*10000) / 10000,
			Unit:        m.Unit,
		}
		bm.diffs[key] = rec
		applied = append(applied, rec)
	}

	return applied, nil
}

// ResetEdits discards uncommitted session changes
func (bm *BinManager) ResetEdits(mapID *int) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if mapID == nil {
		copy(bm.currentData, bm.originalData)
		bm.diffs = make(map[string]DiffRecord)
	} else {
		for k, diff := range bm.diffs {
			if diff.MapID == *mapID {
				end := diff.FileOffset + 4
				if end > len(bm.originalData) {
					end = len(bm.originalData)
				}
				copy(bm.currentData[diff.FileOffset:end], bm.originalData[diff.FileOffset:end])
				delete(bm.diffs, k)
			}
		}
	}
}

// GetAllDiffs returns all modified cells in this session
func (bm *BinManager) GetAllDiffs() []DiffRecord {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	res := make([]DiffRecord, 0, len(bm.diffs))
	for _, d := range bm.diffs {
		res = append(res, d)
	}
	return res
}

// ExportPatchedBin writes the edited binary to a new destination with re-computed CRC-32 where applicable
func (bm *BinManager) ExportPatchedBin(outputPath string) (uint32, error) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	if !bm.loaded {
		return 0, fmt.Errorf("no binary loaded to export")
	}

	// Create output copy
	outData := make([]byte, len(bm.currentData))
	copy(outData, bm.currentData)

	var calcCRC uint32
	if base, hdrOff, ok := tcuImageBase(outData); ok {
		// Recompute Kefico CPTSH 8AT TCU calibration IEEE CRC-32 over [0x1C1280..0x27FD5F] stored BE at 0x1C0244
		crcStartEA := binary.BigEndian.Uint32(outData[hdrOff+0x38 : hdrOff+0x3C])
		crcEndEA := binary.BigEndian.Uint32(outData[hdrOff+0x3C : hdrOff+0x40])
		sOff := int(crcStartEA - base)
		eOff := int(crcEndEA - base)
		if sOff >= 0 && eOff >= sOff && eOff+1 <= len(outData) {
			calcCRC = crc32.ChecksumIEEE(outData[sOff : eOff+1])
			binary.BigEndian.PutUint32(outData[hdrOff+0x44:hdrOff+0x48], calcCRC)
		} else {
			calcCRC = crc32.ChecksumIEEE(outData)
		}
	} else if len(outData) == 0x400000 && bm.isSIM2KDBLocked() {
		// Recompute SIM2K-260 calibration launch-gate CRC-32 ONLY when SIM2K-260 DB is active
		const calStart = 0x200700
		const calEnd = 0x2AE700
		const csAddr = 0x200404
		calcCRC = CRC32MSB(outData[calStart:calEnd])
		binary.LittleEndian.PutUint32(outData[csAddr:csAddr+4], calcCRC)
	} else {
		calcCRC = crc32.ChecksumIEEE(outData)
	}

	// Write to destination
	cleanDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(cleanDir, 0755); err != nil {
		return 0, err
	}

	if err := os.WriteFile(outputPath, outData, 0644); err != nil {
		return 0, err
	}

	fmt.Printf("[BinManager] Exported patched binary (%d B) to '%s' (CRC: 0x%08X)\n", len(outData), outputPath, calcCRC)
	return calcCRC, nil
}

func formatFileSize(size int64) string {
	if size >= 1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(size)/(1024.0*1024.0))
	}
	if size >= 1024 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024.0)
	}
	return fmt.Sprintf("%d B", size)
}

func inferBinCategory(filename, folder string, sz int64) string {
	lowerName := strings.ToLower(filename)
	lowerDir := strings.ToLower(folder)

	if strings.Contains(lowerName, "mdps") || strings.Contains(lowerDir, "mdps") || strings.Contains(lowerName, "eps_") {
		return "MDPS Power Steering ECU"
	}
	if strings.Contains(lowerName, "awd") || strings.Contains(lowerDir, "awd") {
		return "AWD ECU (95447-4J105)"
	}
	if sz == 0xBFC00 || sz == 0xC0000 || strings.Contains(lowerName, "tcu") || strings.Contains(lowerName, "cptsh") || strings.HasPrefix(lowerName, "ekm") || strings.Contains(lowerDir, "tcu") || strings.Contains(lowerDir, "cptsh") {
		return "Kefico CPTSH 8AT TCU (95441-4J571)"
	}
	if sz == 0x400000 {
		if strings.Contains(lowerDir, "claude_bins") {
			return "SIM2K-260 Builds"
		}
		return "SIM2K-260 Engine ECU (4 MB)"
	}
	baseDir := filepath.Base(filepath.Clean(folder))
	if baseDir != "." && baseDir != "" && baseDir != "/" && baseDir != "\\" {
		return fmt.Sprintf("Custom Binary (%s)", baseDir)
	}
	return "Custom ECU Binary"
}

// GetAvailableBins scans workspace and custom directories for all ECU/TCU/AWD/MDPS/custom .bin files
func (bm *BinManager) GetAvailableBins() []AvailableBin {
	bm.mu.RLock()
	var currentActiveAbs string
	if bm.loaded && bm.binPath != "" {
		currentActiveAbs, _ = filepath.Abs(bm.binPath)
	}
	activeDBPath := bm.dbPath
	extraDirs := append([]string(nil), bm.extraScanDirs...)
	extraFiles := append([]string(nil), bm.extraBinPaths...)
	bm.mu.RUnlock()

	type scanEntry struct {
		Dir      string
		Category string
	}

	searchDirs := []scanEntry{
		{"Claude_Bins", ""},
		{"bins", ""},
		{"dumps", ""},
		{".", ""},
		{"../Magna AWD/bins", "AWD ECU (95447-4J105)"},
		{"../Magna AWD", "AWD ECU (95447-4J105)"},
		{"mapping/awd", "AWD ECU (95447-4J105)"},
		{"../Kia SIM2K-260", "Kia SIM2K-260 Root"},
		{"../Kia SIM2K-260/Claude_Bins", "SIM2K-260 Builds"},
		{"../Kia SIM2K-260/dumps", "ECU Dumps"},
		{"C:/Claude Projects/Kefico CPTSH/out", "Kefico CPTSH 8AT TCU (95441-4J571)"},
		{"C:/Claude Projects/TCU Bins", "Kefico CPTSH 8AT TCU (95441-4J571)"},
	}

	// Automatically scan the active DB's directory and common binary subdirectories
	if activeDBPath != "" {
		dbDir := filepath.Dir(activeDBPath)
		for _, sub := range []string{"", "bins", "out", "dumps", "cal", "Claude_Bins"} {
			cand := dbDir
			if sub != "" {
				cand = filepath.Join(dbDir, sub)
			}
			searchDirs = append(searchDirs, scanEntry{Dir: cand, Category: ""})
		}
	}

	// Scan user-registered custom directories and their binary subdirectories
	for _, ed := range extraDirs {
		if ed == "" {
			continue
		}
		for _, sub := range []string{"", "bins", "out", "dumps", "cal"} {
			cand := ed
			if sub != "" {
				cand = filepath.Join(ed, sub)
			}
			searchDirs = append(searchDirs, scanEntry{Dir: cand, Category: ""})
		}
	}

	seen := make(map[string]bool)
	var bins []AvailableBin

	addBinFile := func(fullPath, displayFolder, defaultCat string) {
		absPath, err := filepath.Abs(fullPath)
		if err != nil {
			return
		}
		absKey := strings.ToLower(filepath.Clean(absPath))
		if seen[absKey] {
			return
		}
		fi, err := os.Stat(absPath)
		if err != nil || fi.IsDir() || fi.Size() < 32 {
			return
		}
		seen[absKey] = true

		relPath := filepath.ToSlash(fullPath)
		if rp, err := filepath.Rel(".", absPath); err == nil && !strings.HasPrefix(rp, "..") {
			relPath = filepath.ToSlash(rp)
		} else {
			relPath = filepath.ToSlash(absPath)
		}

		isActive := currentActiveAbs != "" && strings.EqualFold(absPath, currentActiveAbs)
		cat := inferBinCategory(fi.Name(), displayFolder, fi.Size())
		if defaultCat != "" && strings.HasPrefix(cat, "Custom") {
			cat = defaultCat
		}

		folderLbl := filepath.ToSlash(displayFolder)
		if folderLbl == "" {
			folderLbl = filepath.ToSlash(filepath.Dir(relPath))
		}

		bins = append(bins, AvailableBin{
			Path:      relPath,
			Filename:  fi.Name(),
			Folder:    folderLbl,
			SizeBytes: fi.Size(),
			SizeMB:    formatFileSize(fi.Size()),
			ModTime:   fi.ModTime().Format("2006-01-02 15:04"),
			IsActive:  isActive,
			Category:  cat,
		})
	}

	// Include currently loaded binary and explicitly registered custom binary files
	if bm.IsLoaded() && bm.GetBinPath() != "" {
		addBinFile(bm.GetBinPath(), filepath.Dir(bm.GetBinPath()), "")
	}
	for _, ef := range extraFiles {
		if ef != "" {
			addBinFile(ef, filepath.Dir(ef), "")
		}
	}

	for _, sd := range searchDirs {
		var candidateDirs []string
		if filepath.IsAbs(sd.Dir) {
			candidateDirs = []string{sd.Dir}
		} else {
			candidateDirs = []string{sd.Dir, filepath.Join("..", sd.Dir), filepath.Join("../..", sd.Dir)}
		}
		var targetDir string
		for _, cd := range candidateDirs {
			if fi, err := os.Stat(cd); err == nil && fi.IsDir() {
				targetDir = cd
				break
			}
		}
		if targetDir == "" {
			continue
		}

		entries, err := os.ReadDir(targetDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".bin" {
				continue
			}
			addBinFile(filepath.Join(targetDir, e.Name()), targetDir, sd.Category)
		}
	}

	// Sort active first, then bins matching active DB stem/directory, then by mod time descending
	activeDBStem := strings.ToLower(strings.TrimSuffix(filepath.Base(activeDBPath), filepath.Ext(activeDBPath)))
	activeDBDir := ""
	if activeDBPath != "" {
		if absDB, err := filepath.Abs(activeDBPath); err == nil {
			activeDBDir = strings.ToLower(filepath.Dir(absDB))
		}
	}

	binPriority := func(b AvailableBin) int {
		if b.IsActive {
			return 0
		}
		if activeDBDir != "" {
			if absB, err := filepath.Abs(b.Path); err == nil {
				bDir := strings.ToLower(filepath.Dir(absB))
				if bDir != strings.ToLower(mustAbs(".")) && strings.HasPrefix(bDir, activeDBDir) {
					return 1
				}
			}
		}
		if activeDBStem != "" && activeDBStem != "flash_analysis" {
			tok := firstToken(activeDBStem)
			if len(tok) >= 3 && strings.Contains(strings.ToLower(b.Filename), tok) {
				return 2
			}
		}
		return 3
	}

	sort.Slice(bins, func(i, j int) bool {
		pi, pj := binPriority(bins[i]), binPriority(bins[j])
		if pi != pj {
			return pi < pj
		}
		return bins[i].ModTime > bins[j].ModTime
	})

	return bins
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func firstToken(s string) string {
	for _, sep := range []string{"_", "-", ".", " "} {
		if idx := strings.Index(s, sep); idx > 0 {
			return s[:idx]
		}
	}
	return s
}

// SaveUploadedBin writes an uploaded binary file and switches to it
func (bm *BinManager) SaveUploadedBin(filename string, data []byte) (string, error) {
	if len(data) < 32 {
		return "", fmt.Errorf("invalid binary size: %d bytes (must be at least 32 bytes)", len(data))
	}

	cleanName := filepath.Base(filename)
	if !strings.HasSuffix(strings.ToLower(cleanName), ".bin") {
		cleanName += ".bin"
	}

	saveDir := "Claude_Bins"
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		saveDir = "."
	}

	targetPath := filepath.Join(saveDir, cleanName)
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save binary to disk: %w", err)
	}

	if err := bm.Load(targetPath); err != nil {
		return "", err
	}

	return filepath.ToSlash(targetPath), nil
}
