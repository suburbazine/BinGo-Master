package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed static/*
var staticFS embed.FS

func findExistingPath(candidates []string) string {
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

func main() {
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
	dbFlag := flag.String("db", defaultDB, "Path to flash_analysis.db SQLite database")
	binFlag := flag.String("bin", defaultBin, "Path to 4MB calibration binary file")
	flag.Parse()

	absDB, _ := filepath.Abs(*dbFlag)
	absBin, _ := filepath.Abs(*binFlag)

	fmt.Println("================================================================================")
	fmt.Println("       BinGo Master - High-Performance ECU Calibration & Binary Studio          ")
	fmt.Println("================================================================================")
	fmt.Printf("[*] Database File:   %s\n", absDB)
	fmt.Printf("[*] Calibration Bin: %s\n", absBin)
	fmt.Printf("[*] Web Server Port: %d\n", *portFlag)

	// Open DB
	dbStore, err := NewDBStore(*dbFlag)
	if err != nil {
		fmt.Printf("[!] Fatal: Error opening SQLite database: %v\n", err)
		os.Exit(1)
	}
	defer dbStore.Close()

	// Initialize Bin Manager
	binMgr := NewBinManager(*binFlag)

	// Setup Server
	server := NewServer(dbStore, binMgr, *dbFlag)
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

	addr := fmt.Sprintf("127.0.0.1:%d", *portFlag)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown channel
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		fmt.Printf("[*] BinGo Master is live and ready!\n")
		fmt.Printf("[*] URL: http://%s\n", addr)
		fmt.Println("--------------------------------------------------------------------------------")
		fmt.Println("Press Ctrl+C to terminate server.")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[!] HTTP Server error: %v\n", err)
		}
	}()

	<-stopChan
	fmt.Println("\n[*] Shutting down BinGo Master gracefully...")
	httpServer.Close()
	fmt.Println("[*] Server stopped.")
}
