# BinGo Master

**BinGo Master** is a standalone, high-performance web-based ECU calibration editor and binary architecture studio written in Go. It combines deep TriCore (TC1797) disassembly and memory-interaction intelligence with modern, responsive calibration table editing, live rainbow heatmap cell visualization, and drag-and-drop binary management.

Built as a single standalone executable with an embedded, zero-dependency offline web application.

---

## Key Features

### 1. Rainbow Heatmap Calibration Table Editor
* **Dynamic Rainbow Heatmap**: Automatic min/max scaling across 1D curves and 2D/3D map tables with smooth HSL hue interpolation (from deep blue for min values through cyan, emerald green, and amber to coral red for peaks).
* **Scale Legend Bar**: Visual gradient bar showing exact numeric bounds and units for the active table.
* **Cell Hover & Inspection**: Instant tooltips with coordinate indices, raw hex values, and scaled physical engineering units.
* **Precision In-Memory Editing**: Edit values in either Scaled Physical or Raw Hex/Integer formats.
* **Modification Tracking**: Modified cells feature distinct golden glow highlights and visual indicators.
* **Safe Session Management**: In-memory diff staging with one-click map revert or session reset.

### 2. Binary Switcher & Drag-and-Drop Uploader
* **Active Binary Header**: Dedicated status indicator showing the currently loaded calibration image.
* **Binary Switcher Modal**: Automatically discovers and categorizes 4 MB TC1797 binaries across directories with live search filtering, modification timestamps, and file sizes.
* **One-Click Hot-Swapping**: Switch between tunes in memory without restarting the server; open calibration grids refresh immediately.
* **Browser Upload Dropzone**: Drag and drop any 4,194,304-byte flash image directly from your local drive to immediately load and inspect it.
* **Launch-Gate CRC-32 Engine**: Patched binaries can be exported with automated recalculation of the Continental MSB-first CRC-32 checksum (`0x04C11DB7`), guaranteeing that exported bins pass bootloader verification.

### 3. Architecture & Reverse Engineering Suite
* **Flash Partition Dashboard**: Executive metrics and breakdown across `PFLASH0`, `PFLASH1`, `PSPR`, `DSPR`, `LMU`, `DFLASH`, and `SFR_PERIPH`.
* **Cross-Partition Traffic Matrix**: Interactive heat grid of inter-partition memory reads and writes, resolving instruction EAs, caller functions, target symbols, and mnemonics.
* **DFLASH / FEE Inspector**: TC1797 Data Flash EEPROM Emulation analysis across active and passive sectors, block catalogs (VIN, IMMO keys, adaptations, DTC snapshots), and hex/ASCII payload inspection.
* **RAM Variable Dictionary**: Curated engine management state variables with resolved producer (writer) and consumer (reader) routines.
* **UDS Diagnostic DID Manager**: Specification of diagnostic identifiers (DIDs), access permissions, security gating, and firmware entry points.
* **Function & Disassembly Browser**: TriCore 1.6.2 instruction disassembly with interactive caller, callee, and cross-reference graphs.

---

## Getting Started

### Prerequisites
* [Go 1.22+](https://go.dev/dl/)
* CGO compiler (e.g. MinGW-w64 GCC on Windows, GCC/Clang on Linux/macOS)

### Building from Source

```bash
# Clone the repository
git clone https://github.com/suburbazine/bingo-master.git
cd bingo-master

# Build the standalone binary
go build -o bingo_master.exe .
```

On Linux or macOS:
```bash
go build -o bingo_master .
```

---

## Usage

Run the compiled executable directly from your terminal:

```powershell
# Default launch (starts web server on port 8080):
.\bingo_master.exe

# Custom port, database, or binary path:
.\bingo_master.exe -port 8080 -db "mapping/flash_analysis.db" -bin "Claude_Bins/IntensePop_v103_Pops-10.bin"
```

Once running, open your web browser to:
```
http://127.0.0.1:8080
```

> **Note on Database & Binaries**: BinGo Master does not distribute proprietary ECU calibration binaries or closed-source databases. When launched without an existing database file, BinGo Master automatically initializes an empty SQLite schema. Calibration binaries and databases can be mounted via CLI flags or uploaded dynamically through the browser dropzone.

---

## REST API Reference

| Endpoint | Method | Description |
|---|---|---|
| `/api/status` | GET | System health, loaded binary metadata, and diff count |
| `/api/stats` | GET | Global disassembly and partition summary statistics |
| `/api/partitions` | GET | Flash partition architecture and size table |
| `/api/matrix` | GET | Cross-partition memory interaction counts |
| `/api/matrix/interactions` | GET | Detailed boundary traffic between two memory partitions |
| `/api/maps` | GET | Search calibration maps (`q`, `folder`, `type`, `applicability`, `limit`, `page`) |
| `/api/maps/:id` | GET | Map detail, axes, and live binary cell grid |
| `/api/maps/:id/edit` | POST | Apply cell modifications to the in-memory session |
| `/api/maps/:id/reset` | POST | Revert cell edits for a specific map |
| `/api/diffs` | GET | Retrieve all uncommitted cell edits in the current session |
| `/api/diffs/reset` | POST | Discard all in-memory modifications |
| `/api/bin/list` | GET | Discover and list all available 4 MB binaries |
| `/api/bin/select` | POST | Hot-swap active in-memory calibration binary (`{"path": "..."}`) |
| `/api/bin/upload` | POST | Multipart upload of a new 4 MB `.bin` calibration image |
| `/api/bin/export` | POST | Export modified binary with recalculated Launch-Gate CRC-32 |
| `/api/dflash/sectors` | GET | TC1797 FEE Sector 0 vs Sector 1 overview |
| `/api/dflash/blocks` | GET | Full DFLASH block inventory (`sector`, `active`, `category`) |
| `/api/dflash/blocks/:id` | GET | Block payload hex/ASCII dump and wear-leveling timeline |
| `/api/ram/curated` | GET | Curated engine state variables with resolved producers and consumers |
| `/api/ram/inspect/:ea` | GET | Deep producer/consumer inspection of any RAM address |
| `/api/dids` | GET | Catalog of UDS Diagnostic Data Identifiers |
| `/api/functions` | GET | Search disassembled functions (`q`, `partition`, `limit`, `page`) |
| `/api/functions/:ea` | GET | Function detail, TriCore assembly instructions, callers, callees |

---

## Architecture & Design

* **Zero-CDN Single Binary**: All CSS, JavaScript, and HTML templates are embedded directly into the Go executable via `//go:embed static/*`. The server runs 100% offline in isolated garage or dyno environments.
* **Safety & Non-Destructive In-Memory Staging**: Calibration edits are never written to original files on disk until explicitly exported.
* **MSB-First CRC-32 Engine**: Continental TC1797 bootloader verification standard (`0x04C11DB7`) calculation pre-integrated.

---

## License

MIT License. See [LICENSE](LICENSE) for details.
