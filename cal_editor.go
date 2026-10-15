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

// BinManager holds the in-memory binary image and session modifications
type BinManager struct {
	mu           sync.RWMutex
	binPath      string
	originalData []byte
	currentData  []byte
	loaded       bool
	diffs        map[string]DiffRecord // key: "mapID:row:col"
}

// NewBinManager initializes the binary manager
func NewBinManager(binPath string) *BinManager {
	bm := &BinManager{
		binPath: binPath,
		diffs:   make(map[string]DiffRecord),
	}
	if err := bm.Load(binPath); err != nil {
		fmt.Printf("[BinManager] Warning: could not load initial bin '%s': %v\n", binPath, err)
	}
	return bm
}

// Load loads a calibration binary file into memory (4 MB SIM2K-260, 48 KB / 32 KB AWD, etc.)
func (bm *BinManager) Load(path string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 1024 {
		return fmt.Errorf("invalid binary size: %d bytes (must be at least 1,024 bytes)", len(data))
	}

	bm.binPath = path
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

func bytesPerCellForOrg(org string) int {
	switch org {
	case "eFloatLoHi", "eFloatHiLo", "float32", "eLongLoHi", "eLongHiLo", "uint32", "int32":
		return 4
	case "eLoHi", "eSLoHi", "eHiLo", "eSHiLo", "uint16", "int16":
		return 2
	default:
		return 1
	}
}

func (bm *BinManager) resolveMapOffset(m CalMap) int {
	n := len(bm.currentData)
	// If Kefico CPTSH 8AT TCU image (785,408 B at 0x1C0200 or 786,432 B at 0x1C0000) is loaded:
	if base, _, ok := tcuImageBase(bm.currentData); ok && m.EA >= base && m.EA < 0x00280000 {
		return int(m.EA - base)
	}
	// If 32 KB AWD cal-only image (0xC04000..0xC0BFFF) is loaded and map EA is in 0xC04000..0xC0BFFF:
	if n == 0x8000 && m.EA >= 0x00C04000 && m.EA < 0x00C0C000 {
		return int(m.EA - 0x00C04000)
	}
	// If 48 KB AWD combined image (0xC00000..0xC0BFFF) is loaded and map EA is in 0xC00000..0xC0BFFF:
	if n == 0xC000 && m.EA >= 0x00C00000 && m.EA < 0x00C0C000 {
		return int(m.EA - 0x00C00000)
	}
	// If 16 KB AWD RAM image (0xC00000..0xC03FFF) is loaded:
	if n == 0x4000 && m.EA >= 0x00C00000 && m.EA < 0x00C04000 {
		return int(m.EA - 0x00C00000)
	}
	return m.FileOffset
}

func decodeCellValue(buf []byte, org string, factor, offset float64) (int64, float64) {
	if factor == 0 {
		factor = 1.0
	}
	switch org {
	case "eFloatLoHi", "float32":
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
	case "eLongLoHi", "uint32":
		raw := int64(binary.LittleEndian.Uint32(buf[:4]))
		return raw, float64(raw)*factor + offset
	case "eLongHiLo":
		raw := int64(binary.BigEndian.Uint32(buf[:4]))
		return raw, float64(raw)*factor + offset
	case "int32":
		raw := int64(int32(binary.LittleEndian.Uint32(buf[:4])))
		return raw, float64(raw)*factor + offset
	case "eHiLo":
		raw := int64(binary.BigEndian.Uint16(buf[:2]))
		return raw, float64(raw)*factor + offset
	case "eSHiLo":
		raw := int64(int16(binary.BigEndian.Uint16(buf[:2])))
		return raw, float64(raw)*factor + offset
	case "eSLoHi", "int16":
		raw := int64(int16(binary.LittleEndian.Uint16(buf[:2])))
		return raw, float64(raw)*factor + offset
	case "eLoHi", "uint16":
		raw := int64(binary.LittleEndian.Uint16(buf[:2]))
		return raw, float64(raw)*factor + offset
	case "eSByte", "int8":
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
		Map:      m,
		XAxis:    make([]AxisPoint, 0),
		YAxis:    make([]AxisPoint, 0),
		Cells:    make([][]CellPoint, 0),
		IsEdited: false,
	}

	if !bm.loaded {
		return resp, fmt.Errorf("no binary loaded")
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
		return resp, fmt.Errorf("map offset 0x%X (EA 0x%X) out of binary range (%d bytes loaded)", baseOffset, m.EA, len(bm.currentData))
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

	// Read X-Axis
	resp.XAxis = bm.readAxis(m.AxisXEA, cols, m.AxisXName, m.DataOrg)

	// Read Y-Axis
	resp.YAxis = bm.readAxis(m.AxisYEA, rows, m.AxisYName, m.DataOrg)

	return resp, nil
}

func (bm *BinManager) readAxis(axisEA *uint32, count int, name string, dataOrg string) []AxisPoint {
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
	if axisEA == nil {
		return defaultAxis()
	}

	var axOffset int = -1
	ea := *axisEA
	n := len(bm.currentData)

	// Kefico CPTSH 8AT TCU Big-Endian calibration partition (0x001C0000..0x0027FFFF)
	if base, _, ok := tcuImageBase(bm.currentData); ok && ea >= base && ea < 0x00280000 {
		axOffset = int(ea - base)
		if bytesPerCellForOrg(dataOrg) == 1 && axOffset+count <= n {
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

	if ea >= 0x80800000 && ea < 0x80A00000 {
		axOffset = int(ea - 0x80800000 + 0x200000)
	} else if ea >= 0x00C00000 && ea < 0x00C0C000 {
		if n == 0x8000 && ea >= 0x00C04000 {
			axOffset = int(ea - 0x00C04000)
		} else {
			axOffset = int(ea - 0x00C00000)
		}
	}
	if axOffset < 0 {
		return defaultAxis()
	}

	if axOffset+count*2 <= len(bm.currentData) {
		// Read as uint16 LE
		for i := 0; i < count; i++ {
			val := int(binary.LittleEndian.Uint16(bm.currentData[axOffset+i*2 : axOffset+i*2+2]))
			axis[i] = AxisPoint{
				Index:    i,
				Raw:      val,
				Physical: float64(val),
				Label:    fmt.Sprintf("%d", val),
			}
		}
	} else if axOffset+count <= len(bm.currentData) {
		// Read as uint8
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
	bytesPerCell := bytesPerCellForOrg(m.DataOrg)
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

		oldRaw, oldPhys := decodeCellValue(bm.currentData[cellOffset:cellOffset+bytesPerCell], m.DataOrg, factor, m.Offset)

		var newRaw int64
		var newPhys float64

		if m.DataOrg == "eFloatLoHi" || m.DataOrg == "eFloatHiLo" || m.DataOrg == "float32" {
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
			if m.DataOrg == "eFloatHiLo" {
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
				if m.DataOrg == "eSByte" || m.DataOrg == "int8" {
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
				if m.DataOrg == "eSLoHi" || m.DataOrg == "int16" || m.DataOrg == "eSHiLo" {
					if newRaw < -32768 {
						newRaw = -32768
					}
					if newRaw > 32767 {
						newRaw = 32767
					}
					if m.DataOrg == "eSHiLo" {
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
					if m.DataOrg == "eHiLo" {
						binary.BigEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(newRaw))
					} else {
						binary.LittleEndian.PutUint16(bm.currentData[cellOffset:cellOffset+2], uint16(newRaw))
					}
				}
			case 4:
				if m.DataOrg == "eLongHiLo" {
					binary.BigEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(newRaw))
				} else {
					binary.LittleEndian.PutUint32(bm.currentData[cellOffset:cellOffset+4], uint32(newRaw))
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

// ExportPatchedBin writes the edited binary to a new destination with re-computed CRC-32
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
	} else if len(outData) == 0x400000 {
		// Recompute SIM2K-260 calibration launch-gate CRC-32
		const calStart = 0x200700
		const calEnd = 0x2AE700
		const csAddr = 0x200404
		calcCRC = CRC32MSB(outData[calStart:calEnd])
		binary.LittleEndian.PutUint32(outData[csAddr:csAddr+4], calcCRC)
	} else {
		calcCRC = CRC32MSB(outData)
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
	return fmt.Sprintf("%.1f KB", float64(size)/1024.0)
}

// GetAvailableBins scans workspace directories for SIM2K-260, AWD ECU, and Kefico CPTSH TCU binary files
func (bm *BinManager) GetAvailableBins() []AvailableBin {
	bm.mu.RLock()
	currentActiveAbs, _ := filepath.Abs(bm.binPath)
	bm.mu.RUnlock()

	searchDirs := []struct {
		Dir      string
		Category string
	}{
		{"Claude_Bins", "Claude Builds"},
		{"dumps", "ECU Dumps"},
		{"mapping/awd", "AWD ECU (95447-4J105)"},
		{".", "Root / Base Bins"},
		{"../Kia SIM2K-260", "Kia SIM2K-260 Root"},
		{"../Kia SIM2K-260/Claude_Bins", "Claude Builds"},
		{"../Kia SIM2K-260/dumps", "ECU Dumps"},
		{"C:/Claude Projects/Kefico CPTSH/out", "Kefico CPTSH 8AT TCU (95441-4J571)"},
		{"C:/Claude Projects/TCU Bins", "Kefico CPTSH 8AT TCU (95441-4J571)"},
	}

	seen := make(map[string]bool)
	var bins []AvailableBin

	for _, sd := range searchDirs {
		candidateDirs := []string{sd.Dir, filepath.Join("..", sd.Dir), filepath.Join("../..", sd.Dir)}
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
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".bin") {
				continue
			}

			fullPath := filepath.Join(targetDir, e.Name())
			absPath, err := filepath.Abs(fullPath)
			if err != nil {
				continue
			}
			absKey := strings.ToLower(filepath.Clean(absPath))
			if seen[absKey] {
				continue
			}

			fi, err := e.Info()
			if err != nil {
				continue
			}

			sz := fi.Size()
			lowerName := strings.ToLower(e.Name())
			// Accept 4 MB SIM2K-260 bins, 16KB/32KB/48KB AWD ECU bins, and 767KB/768KB Kefico CPTSH TCU bins
			isAwd := strings.HasPrefix(lowerName, "awd_") || sz == 0xC000 || sz == 0x8000 || sz == 0x4000
			isTcu := sz == 0xBFC00 || sz == 0xC0000 || strings.HasPrefix(lowerName, "tcu") || strings.HasPrefix(lowerName, "ekm")
			if sz != 0x400000 && !isAwd && !isTcu {
				continue
			}

			seen[absKey] = true
			relPath := filepath.ToSlash(fullPath)
			if rp, err := filepath.Rel(".", fullPath); err == nil && !strings.HasPrefix(rp, "..") {
				relPath = filepath.ToSlash(rp)
			}

			isActive := strings.EqualFold(absPath, currentActiveAbs)
			cat := sd.Category
			if isTcu {
				cat = "Kefico CPTSH 8AT TCU (95441-4J571)"
			} else if isAwd {
				cat = "AWD ECU (95447-4J105)"
			}

			bins = append(bins, AvailableBin{
				Path:      relPath,
				Filename:  e.Name(),
				Folder:    sd.Dir,
				SizeBytes: sz,
				SizeMB:    formatFileSize(sz),
				ModTime:   fi.ModTime().Format("2006-01-02 15:04"),
				IsActive:  isActive,
				Category:  cat,
			})
		}
	}

	// Sort active first, then by mod time descending
	sort.Slice(bins, func(i, j int) bool {
		if bins[i].IsActive != bins[j].IsActive {
			return bins[i].IsActive
		}
		return bins[i].ModTime > bins[j].ModTime
	})

	return bins
}

// SaveUploadedBin writes an uploaded binary file and switches to it
func (bm *BinManager) SaveUploadedBin(filename string, data []byte) (string, error) {
	if len(data) < 1024 {
		return "", fmt.Errorf("invalid binary size: %d bytes (must be at least 1,024 bytes)", len(data))
	}

	cleanName := filepath.Base(filename)
	if !strings.HasSuffix(strings.ToLower(cleanName), ".bin") {
		cleanName += ".bin"
	}

	saveDir := "Claude_Bins"
	if _, err := os.Stat(saveDir); os.IsNotExist(err) {
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


