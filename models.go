package main

// Partition represents a physical or logical memory partition in TC1797
type Partition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	StartEA     uint32 `json:"start_ea"`
	StartEAHex  string `json:"start_ea_hex"`
	EndEA       uint32 `json:"end_ea"`
	EndEAHex    string `json:"end_ea_hex"`
	FileStart   *int   `json:"file_start"`
	FileEnd     *int   `json:"file_end"`
	MemoryType  string `json:"memory_type"`
	Description string `json:"description"`
	FnCount     int    `json:"fn_count"`
	InsnCount   int    `json:"insn_count"`
	SizeKB      int    `json:"size_kb"`
}

// Stats holds high-level statistics for the dashboard
type Stats struct {
	InstructionCount  int `json:"instruction_count"`
	FunctionCount     int `json:"function_count"`
	CallCount         int `json:"call_count"`
	MemoryInteractions int `json:"memory_interactions"`
	MapCount          int `json:"map_count"`
	DFlashBlockCount  int `json:"dflash_block_count"`
	SymbolCount       int `json:"symbol_count"`
	PartitionCount    int `json:"partition_count"`
}

// MatrixCell represents an element in the cross-partition interaction matrix
type MatrixCell struct {
	SourcePartition string `json:"source_partition"`
	TargetPartition string `json:"target_partition"`
	Count           int    `json:"count"`
}

// MemoryInteraction represents a single decoded memory access
type MemoryInteraction struct {
	ID              int    `json:"id"`
	InsnEA          uint32 `json:"insn_ea"`
	InsnEAHex       string `json:"insn_ea_hex"`
	FunctionEA      uint32 `json:"function_ea"`
	FunctionEAHex   string `json:"function_ea_hex"`
	FunctionName    string `json:"function_name"`
	SourcePartition string `json:"source_partition"`
	TargetEA        uint32 `json:"target_ea"`
	TargetEAHex     string `json:"target_ea_hex"`
	TargetPartition string `json:"target_partition"`
	AccessType      string `json:"access_type"`
	AccessWidth     int    `json:"access_width"`
	TargetSymbol    string `json:"target_symbol"`
	Mnemonic        string `json:"mnemonic"`
	OpStr           string `json:"op_str"`
	IsCross         bool   `json:"is_cross"`
}

// CalMap represents a calibration map/curve/scalar
type CalMap struct {
	ID           int      `json:"id"`
	Name         string   `json:"name"`
	FileOffset   int      `json:"file_offset"`
	FileOffsetHex string  `json:"file_offset_hex"`
	EA           uint32   `json:"ea"`
	EAHex        string   `json:"ea_hex"`
	Folder       string   `json:"folder"`
	MapType      string   `json:"map_type"`
	Columns      int      `json:"columns"`
	Rows         int      `json:"rows"`
	DataOrg      string   `json:"data_org"`
	Unit         string   `json:"unit"`
	Factor       float64  `json:"factor"`
	Offset       float64  `json:"offset"`
	AxisXEA           *uint32  `json:"axis_x_ea"`
	AxisXEAHex        string   `json:"axis_x_ea_hex"`
	AxisXName         string   `json:"axis_x_name"`
	AxisYEA           *uint32  `json:"axis_y_ea"`
	AxisYEAHex        string   `json:"axis_y_ea_hex"`
	AxisYName         string   `json:"axis_y_name"`
	Comment           string   `json:"comment"`
	RefCount          int      `json:"ref_count"`
	RefFunctions      string   `json:"ref_functions"`
	RefFnList         []string `json:"ref_fn_list"`
	Applicability     string   `json:"applicability"`
	ApplicabilityNote string   `json:"applicability_note"`
}

// MapDataResponse contains the map descriptor, axes, and 2D grid of values
type MapDataResponse struct {
	Map      CalMap        `json:"map"`
	XAxis    []AxisPoint   `json:"x_axis"`
	YAxis    []AxisPoint   `json:"y_axis"`
	Cells    [][]CellPoint `json:"cells"`
	IsEdited bool          `json:"is_edited"`
}

// AxisPoint holds coordinate header info
type AxisPoint struct {
	Index    int     `json:"index"`
	Raw      int     `json:"raw"`
	Physical float64 `json:"physical"`
	Label    string  `json:"label"`
}

// CellPoint holds the raw integer and physical scaled float for a map cell
type CellPoint struct {
	Row         int     `json:"row"`
	Col         int     `json:"col"`
	FileOffset  int     `json:"file_offset"`
	Raw         int64   `json:"raw"`
	Physical    float64 `json:"physical"`
	OriginalRaw int64   `json:"original_raw"`
	IsModified  bool    `json:"is_modified"`
}

// CellEditRequest holds user edits
type CellEditRequest struct {
	MapID int         `json:"map_id"`
	Edits []CellEdit  `json:"edits"`
}

// CellEdit represents one modified cell
type CellEdit struct {
	Row      int     `json:"row"`
	Col      int     `json:"col"`
	Raw      *int64  `json:"raw"`
	Physical *float64 `json:"physical"`
}

// DFlashSector holds overview of Sector 0 and Sector 1
type DFlashSector struct {
	SectorIndex   int    `json:"sector_index"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	StartOffset   int    `json:"start_offset"`
	EndOffset     int    `json:"end_offset"`
	SizeKB        int    `json:"size_kb"`
	BlockCount    int    `json:"block_count"`
	ActiveBlocks  int    `json:"active_blocks"`
}

// DFlashBlock holds metadata and payload for a single EEPROM block
type DFlashBlock struct {
	ID           int    `json:"id"`
	SectorIdx    int    `json:"sector_idx"`
	BlockID      int    `json:"block_id"`
	BlockIDHex   string `json:"block_id_hex"`
	Offset       int    `json:"offset"`
	OffsetHex    string `json:"offset_hex"`
	DFlashEA     uint32 `json:"dflash_ea"`
	DFlashEAHex  string `json:"dflash_ea_hex"`
	VersionCount int    `json:"version_count"`
	IsActive     bool   `json:"is_active"`
	Length       int    `json:"length"`
	CRC          int    `json:"crc"`
	CRCHex       string `json:"crc_hex"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	PayloadHex   string `json:"payload_hex"`
	PayloadASCII string `json:"payload_ascii"`
}

// DFlashBlockDetail includes the active block plus all historical wear-leveling revisions
type DFlashBlockDetail struct {
	ActiveBlock DFlashBlock   `json:"active_block"`
	Revisions   []DFlashBlock `json:"revisions"`
}

// RAMSymbol represents an engine state variable
type RAMSymbol struct {
	EA          uint32   `json:"ea"`
	EAHex       string   `json:"ea_hex"`
	Name        string   `json:"name"`
	Partition   string   `json:"partition"`
	Description string   `json:"description"`
	DataType    string   `json:"data_type"`
	Unit        string   `json:"unit"`
	Scaling     string   `json:"scaling"`
	TotalAccess int      `json:"total_access"`
	Reads       int      `json:"reads"`
	Writes      int      `json:"writes"`
	Producers   []FnLink `json:"producers"`
	Consumers   []FnLink `json:"consumers"`
}

// FnLink holds a reference to a function accessing RAM or called
type FnLink struct {
	FunctionEA   uint32 `json:"function_ea"`
	FunctionEAHex string `json:"function_ea_hex"`
	FunctionName string `json:"function_name"`
	Partition    string `json:"partition"`
	InsnEA       uint32 `json:"insn_ea"`
	InsnEAHex    string `json:"insn_ea_hex"`
	AccessType   string `json:"access_type"`
	Mnemonic     string `json:"mnemonic"`
	OpStr        string `json:"op_str"`
}

// UDSDid represents a diagnostic data identifier
type UDSDid struct {
	DID         string `json:"did"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Access      string `json:"access"`
	Sessions    string `json:"sessions"`
	Security    string `json:"security"`
	MemoryTarget string `json:"memory_target"`
	HandlerEA   string `json:"handler_ea"`
	HandlerName string `json:"handler_name"`
	DataFormat  string `json:"data_format"`
	LengthBytes int    `json:"length_bytes"`
}

// Function represents a disassembled function
type Function struct {
	EA            uint32 `json:"ea"`
	EAHex         string `json:"ea_hex"`
	Name          string `json:"name"`
	PartitionName string `json:"partition_name"`
	EndEA         uint32 `json:"end_ea"`
	EndEAHex      string `json:"end_ea_hex"`
	SizeBytes     int    `json:"size_bytes"`
	InsnCount     int    `json:"insn_count"`
	HasReturn     bool   `json:"has_return"`
	CallerCount   int    `json:"caller_count"`
	CalleeCount   int    `json:"callee_count"`
	MemReadCount  int    `json:"mem_read_count"`
	MemWriteCount int    `json:"mem_write_count"`
}

// Instruction represents a single decoded TriCore instruction
type Instruction struct {
	EA            uint32 `json:"ea"`
	EAHex         string `json:"ea_hex"`
	FunctionEA    uint32 `json:"function_ea"`
	PartitionName string `json:"partition_name"`
	Mnemonic      string `json:"mnemonic"`
	OpStr         string `json:"op_str"`
	SizeBytes     int    `json:"size_bytes"`
	TargetEA      uint32 `json:"target_ea,omitempty"`
	TargetEAHex   string `json:"target_ea_hex,omitempty"`
	TargetSymbol  string `json:"target_symbol,omitempty"`
	IsCall        bool   `json:"is_call"`
	CallType      string `json:"call_type,omitempty"`
}

// FunctionDetail contains instructions, callers, callees, and memory accesses
type FunctionDetail struct {
	Function     Function            `json:"function"`
	Instructions []Instruction       `json:"instructions"`
	Callers      []CallEdge          `json:"callers"`
	Callees      []CallEdge          `json:"callees"`
	MemoryAccess []MemoryInteraction `json:"memory_access"`
}

// CallEdge represents a call link
type CallEdge struct {
	CallerEA        uint32 `json:"caller_ea"`
	CallerEAHex     string `json:"caller_ea_hex"`
	CallerFn        uint32 `json:"caller_fn"`
	CallerFnHex     string `json:"caller_fn_hex"`
	CallerFnName    string `json:"caller_fn_name"`
	CallerPartition string `json:"caller_partition"`
	CalleeEA        uint32 `json:"callee_ea"`
	CalleeEAHex     string `json:"callee_ea_hex"`
	CalleeFn        uint32 `json:"callee_fn"`
	CalleeFnHex     string `json:"callee_fn_hex"`
	CalleeFnName    string `json:"callee_fn_name"`
	CalleePartition string `json:"callee_partition"`
	CallType        string `json:"call_type"`
	IsCross         bool   `json:"is_cross"`
}

// DiffRecord represents an uncommitted in-memory bin modification
type DiffRecord struct {
	MapID       int     `json:"map_id"`
	MapName     string  `json:"map_name"`
	FileOffset  int     `json:"file_offset"`
	Row         int     `json:"row"`
	Col         int     `json:"col"`
	OldRaw      int64   `json:"old_raw"`
	NewRaw      int64   `json:"new_raw"`
	OldPhysical float64 `json:"old_physical"`
	NewPhysical float64 `json:"new_physical"`
	Unit        string  `json:"unit"`
}

// AvailableBin represents a discovered 4MB calibration binary file
type AvailableBin struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Folder    string `json:"folder"`
	SizeBytes int64  `json:"size_bytes"`
	SizeMB    string `json:"size_mb"`
	ModTime   string `json:"mod_time"`
	IsActive  bool   `json:"is_active"`
	Category  string `json:"category"`
}

// BinListResponse is the API response for /api/bin/list
type BinListResponse struct {
	ActivePath string         `json:"active_path"`
	ActiveName string         `json:"active_name"`
	Bins       []AvailableBin `json:"bins"`
}

