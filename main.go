package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

//go:embed static/*
var staticFS embed.FS

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func findExistingPath(candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

func resolveUserFlagPath(p, origWD string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if origWD != "" {
		cand := filepath.Join(origWD, p)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return p
}

func main() {
	origWD, _ := os.Getwd()

	// Ensure working directory is executable's directory if launched from a shortcut
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		if _, statErr := os.Stat("flash_analysis.db"); statErr != nil {
			if _, exeStatErr := os.Stat(filepath.Join(exeDir, "flash_analysis.db")); exeStatErr == nil {
				_ = os.Chdir(exeDir)
			}
		}
	}

	defaultDB := findExistingPath([]string{
		"flash_analysis.db",
		"mapping/flash_analysis.db",
		"../Kia SIM2K-260/mapping/flash_analysis.db",
		"../../mapping/flash_analysis.db",
		"../mapping/flash_analysis.db",
		"tcu_cptsh_95441_4J571.db",
		"awd_95447_4J105.db",
	})

	defaultBin := findExistingPath([]string{
		"IntensePop_v103_Pops-10.bin",
		"Claude_Bins/IntensePop_v103_Pops-10.bin",
		"../Kia SIM2K-260/Claude_Bins/IntensePop_v103_Pops-10.bin",
		"../../Claude_Bins/IntensePop_v103_Pops-10.bin",
		"../Claude_Bins/IntensePop_v103_Pops-10.bin",
		"CKHK_3TMS2C7C_SLEEPY_DECAT_QUIET_GOOD.bin",
		"../../CKHK_3TMS2C7C_SLEEPY_DECAT_QUIET_GOOD.bin",
		"tcu_cptsh_stock_read_complete.bin",
		"awd_95447_4J105_C00000_C0BFFF.bin",
		"calibration.bin",
	})

	portFlag := flag.Int("port", 8080, "Port for BinGo Master web server")
	dbFlag := flag.String("db", defaultDB, "Path to ECU SQLite analysis database (.db)")
	binFlag := flag.String("bin", defaultBin, "Path to calibration/firmware binary file (.bin)")
	openFlag := flag.Bool("open", false, "Automatically open BinGo Master in the default web browser")
	flag.Parse()

	explicitDB := false
	explicitBin := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "db" {
			explicitDB = true
		}
		if f.Name == "bin" {
			explicitBin = true
		}
	})

	resolvedDB := resolveUserFlagPath(*dbFlag, origWD)
	resolvedBin := resolveUserFlagPath(*binFlag, origWD)
	if explicitDB && !explicitBin && !isBuiltinDBName(filepath.Base(resolvedDB)) {
		// Don't preload a default SIM2K binary onto an explicitly requested custom DB; let Server auto-match companion bins
		resolvedBin = ""
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *portFlag)
	url := fmt.Sprintf("http://%s", addr)

	// If -open is set and a BinGo Master instance is already listening on addr, just open the browser
	if *openFlag {
		if conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
			_ = conn.Close()
			fmt.Printf("[*] BinGo Master is already running at %s — opening browser...\n", url)
			openBrowser(url)
			return
		}
	}

	absDB, _ := filepath.Abs(resolvedDB)
	absBin := "(auto-detect)"
	if resolvedBin != "" {
		absBin, _ = filepath.Abs(resolvedBin)
	}

	fmt.Println("================================================================================")
	fmt.Println("       BinGo Master - High-Performance ECU Calibration & Binary Studio          ")
	fmt.Println("================================================================================")
	fmt.Printf("[*] Database File:   %s\n", absDB)
	fmt.Printf("[*] Calibration Bin: %s\n", absBin)
	fmt.Printf("[*] Web Server Port: %d\n", *portFlag)

	// Open DB
	dbStore, err := NewDBStore(resolvedDB)
	if err != nil {
		fmt.Printf("[!] Fatal: Error opening SQLite database: %v\n", err)
		os.Exit(1)
	}
	defer dbStore.Close()

	// Initialize Bin Manager
	binMgr := NewBinManager(resolvedBin)

	// Setup Server
	server := NewServer(dbStore, binMgr, resolvedDB)
	if explicitDB && !explicitBin && !isBuiltinDBName(filepath.Base(resolvedDB)) {
		_ = server.switchDatabase(resolvedDB, true)
	}
	mux := http.NewServeMux()

	// Register API routes
	server.RegisterRoutes(mux)

	// Embedded Static Files
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		fmt.Printf("[!] Fatal: Error accessing embedded static assets: %v\n", err)
		os.Exit(1)
	}

	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		fmt.Printf("[*] BinGo Master is live and ready!\n")
		fmt.Printf("[*] URL: %s\n", url)
		fmt.Println("--------------------------------------------------------------------------------")
		fmt.Println("Press Ctrl+C to terminate server.")
		if *openFlag {
			go func() {
				time.Sleep(350 * time.Millisecond)
				openBrowser(url)
			}()
		}
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[!] HTTP Server error: %v\n", err)
		}
	}()

	<-stopChan
	fmt.Println("\n[*] Shutting down BinGo Master gracefully...")
	httpServer.Close()
	fmt.Println("[*] Server stopped.")
}
