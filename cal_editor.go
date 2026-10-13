package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

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

// Load loads a 4 MB binary file into memory
func (bm *BinManager) Load(path string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) != 0x400000 {
		return fmt.Errorf("invalid binary size: %d bytes (expected 4,194,304 bytes)", len(data))
	}

	bm.binPath = path
	bm.originalData = make([]byte, len(data))
	copy(bm.originalData, data)
	bm.currentData = make([]byte, len(data))
	copy(bm.currentData, data)
	bm.diffs = make(map[string]DiffRecord)
	bm.loaded = true

	fmt.Printf("[BinManager] Successfully loaded 4MB calibration binary: %s\n", path)
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

	bytesPerCell := 1
	if m.DataOrg == "eLoHi" || m.DataOrg == "eHiLo" {
		bytesPerCell = 2
	}

	totalBytes := cols * rows * bytesPerCell
	if m.FileOffset < 0 || m.FileOffset+totalBytes > len(bm.currentData) {
		return resp, fmt.Errorf("map offset 0x%X out of binary range", m.FileOffset)
	}

	// Read Cells
	resp.Cells = make([][]CellPoint, rows)
	for r := 0; r < rows; r++ {
		resp.Cells[r] = make([]CellPoint, cols)
		for c := 0; c < cols; c++ {
			cellOffset := m.FileOffset + (r*cols+c)*bytesPerCell

			var raw int64
			var origRaw int64
			if bytesPerCell == 1 {
				raw = int64(bm.currentData[cellOffset])
				origRaw = int64(bm.originalData[cellOffset])
			} else {
				if m.DataOrg == "eHiLo" {
					raw = int64(binary.BigEndian.Uint16(bm.currentData[cellOffset : cellOffset+2]))
					origRaw = int64(binary.BigEndian.Uint16(bm.originalData[cellOffset : cellOffset+2]))
				} else {
					raw = int64(binary.LittleEndian.Uint16(bm.currentData[cellOffset : cellOffset+2]))
					origRaw = int64(binary.LittleEndian.Uint16(bm.originalData[cellOffset : cellOffset+2]))
				}
			}

			factor := m.Factor
			if factor == 0 {
				factor = 1.0
			}
			physical := float64(raw)*factor + m.Offset

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
				Physical:    math.Round(physical*1000) / 1000,
				OriginalRaw: origRaw,
				IsModified:  isMod,
			}
		}
	}

	// Read X-Axis
	resp.XAxis = bm.readAxis(m.AxisXEA, cols, m.AxisXName)

	// Read Y-Axis
	resp.YAxis = bm.readAxis(m.AxisYEA, rows, m.AxisYName)

	return resp, nil
}

func (bm *BinManager) readAxis(axisEA *uint32, count int, name string) []AxisPoint {
	axis := make([]AxisPoint, count)
	if axisEA == nil || *axisEA < 0x80800000 || *axisEA >= 0x80A00000 {
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

	axOffset := int(*axisEA - 0x80800000 + 0x200000)
	if axOffset+count*2 <= len(bm.currentData) {
		// Try reading as uint16 LE
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
		for i := 0; i < count; i++ {
			axis[i] = AxisPoint{
				Index:    i,
				Raw:      i,
				Physical: float64(i),
				Label:    fmt.Sprintf("[%d]", i),
			}
		}
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
	bytesPerCell := 1
	if m.DataOrg == "eLoHi" || m.DataOrg == "eHiLo" {
		bytesPerCell = 2
	}

	factor := m.Factor
	if factor == 0 {
		factor = 1.0
	}

	var applied []DiffRecord

	for _, edit := range edits {
		if edit.Row < 0 || edit.Row >= m.Rows || edit.Col < 0 || edit.Col >= m.Columns {
			continue
		}

		cellOffset := m.FileOffset + (edit.Row*cols+edit.Col)*bytesPerCell
		if cellOffset+bytesPerCell > len(bm.currentData) {
			continue
		}

		var oldRaw int64
		if bytesPerCell == 1 {
			oldRaw = int64(bm.currentData[cellOffset])
		} else {
			if m.DataOrg == "eHiLo" {
				oldRaw = int64(binary.BigEndian.Uint16(bm.currentData[cellOffset : cellOffset+2]))
			} else {
				oldRaw = int64(binary.LittleEndian.Uint16(bm.currentData[cellOffset : cellOffset+2]))
			}
		}
		oldPhys := float64(oldRaw)*factor + m.Offset

		var newRaw int64
		var newPhys float64

		if edit.Raw != nil {
			newRaw = *edit.Raw
			newPhys = float64(newRaw)*factor + m.Offset
		} else if edit.Physical != nil {
			newPhys = *edit.Physical
			newRaw = int64(math.Round((newPhys - m.Offset) / factor))
		} else {
			continue
		}

		// Clamp to data size
		if bytesPerCell == 1 {
			if newRaw < 0 {
				newRaw = 0
			}
			if newRaw > 255 {
				newRaw = 255
			}
			bm.currentData[cellOffset] = byte(newRaw)
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

		key := fmt.Sprintf("%d:%d:%d", m.ID, edit.Row, edit.Col)
		rec := DiffRecord{
			MapID:       m.ID,
			MapName:     m.Name,
			FileOffset:  cellOffset,
			Row:         edit.Row,
			Col:         edit.Col,
			OldRaw:      oldRaw,
			NewRaw:      newRaw,
			OldPhysical: math.Round(oldPhys*1000) / 1000,
			NewPhysical: math.Round(newPhys*1000) / 1000,
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
		prefix := fmt.Sprintf("%d:", *mapID)
		for k, diff := range bm.diffs {
			if diff.MapID == *mapID {
				// Revert bytes
				copy(bm.currentData[diff.FileOffset:], bm.originalData[diff.FileOffset:diff.FileOffset+2])
				delete(bm.diffs, k)
			}
			_ = prefix
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

	// Recompute calibration launch-gate CRC-32
	// Range: file 0x200700..0x2AE6FF (ECU 0xA0800700..0xA08AE6FF)
	const calStart = 0x200700
	const calEnd = 0x2AE700
	const csAddr = 0x200404

	if len(outData) < calEnd {
		return 0, fmt.Errorf("binary data too small for calibration CRC calculation")
	}

	calcCRC := CRC32MSB(outData[calStart:calEnd])
	binary.LittleEndian.PutUint32(outData[csAddr:csAddr+4], calcCRC)

	// Write to destination
	cleanDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(cleanDir, 0755); err != nil {
		return 0, err
	}

	if err := os.WriteFile(outputPath, outData, 0644); err != nil {
		return 0, err
	}

	fmt.Printf("[BinManager] Exported patched binary to '%s' with corrected Launch-Gate CRC: 0x%08X\n", outputPath, calcCRC)
	return calcCRC, nil
}

// GetAvailableBins scans Claude_Bins, dumps, and root for 4MB binary files
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
		{".", "Root / Base Bins"},
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
			if err != nil || seen[absPath] {
				continue
			}

			fi, err := e.Info()
			if err != nil {
				continue
			}

			if fi.Size() != 0x400000 {
				continue
			}

			seen[absPath] = true
			relPath := fullPath
			if rp, err := filepath.Rel(".", fullPath); err == nil && !strings.HasPrefix(rp, "..") {
				relPath = filepath.ToSlash(rp)
			} else {
				relPath = filepath.ToSlash(fullPath)
			}

			isActive := (absPath == currentActiveAbs)

			bins = append(bins, AvailableBin{
				Path:      relPath,
				Filename:  e.Name(),
				Folder:    sd.Dir,
				SizeBytes: fi.Size(),
				SizeMB:    "4.00 MB",
				ModTime:   fi.ModTime().Format("2006-01-02 15:04"),
				IsActive:  isActive,
				Category:  sd.Category,
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

// SaveUploadedBin writes an uploaded binary file to Claude_Bins and switches to it
func (bm *BinManager) SaveUploadedBin(filename string, data []byte) (string, error) {
	if len(data) != 0x400000 {
		return "", fmt.Errorf("invalid binary size: %d bytes (expected exactly 4,194,304 bytes)", len(data))
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

