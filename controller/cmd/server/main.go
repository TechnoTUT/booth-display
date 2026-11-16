package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"booth-display/controller/internal/api"
	"booth-display/controller/internal/config"
	"booth-display/controller/internal/core"
)

func main() {
	configPath := flag.String("config", "../config.yaml", "Path to config.yaml")
	httpPort := flag.Int("port", 0, "Override HTTP port")
	autoStart := flag.Bool("auto-start", false, "Automatically start streaming on launch")
	corePath := flag.String("core", "", "Path to the booth-core media core binary (default: next to this executable, then PATH)")
	flag.Parse()

	log.Println("=========================================")
	log.Println("  booth-display controller starting...   ")
	log.Println("=========================================")

	// 1. Load Config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		// Fallback check in current dir
		fallbackPath := "config.yaml"
		log.Printf("Could not load from %s: %v. Trying %s...", *configPath, err, fallbackPath)
		cfg, err = config.LoadConfig(fallbackPath)
		if err != nil {
			log.Fatalf("Fatal: unable to load config: %v", err)
		}
	}

	if *httpPort > 0 {
		cfg.Server.HTTPPort = *httpPort
	}
	snap := cfg.GetSnapshot()

	log.Printf("Loaded config with %d displays (Canvas: %dx%d @ %dfps)",
		len(snap.Displays), snap.Canvas.Width, snap.Canvas.Height, snap.Canvas.FPS)
	for i, d := range snap.Displays {
		log.Printf("  [%d] %s (%s) -> Target %s:%d, Crop [%d,%d %dx%d], BezelRight: %dpx",
			i+1, d.Name, d.ID, d.IP, d.Port, d.CropX, d.CropY, d.Width, d.Height, d.BezelPaddingRight)
	}

	// 2. Start the media core (Rust) and hand it the configuration
	coreBin, err := core.FindBinary(*corePath)
	if err != nil {
		log.Fatalf("Fatal: %v", err)
	}
	mediaCore := core.New(coreBin)
	if err := mediaCore.Start(); err != nil {
		log.Fatalf("Fatal: %v", err)
	}
	defer mediaCore.Close()
	if err := mediaCore.Configure(cfg); err != nil {
		log.Fatalf("Fatal: failed to configure the media core: %v", err)
	}

	// 3. Initialize HTTP & Web-GUI API Server
	srv := api.NewServer(cfg, mediaCore)

	// Auto-start streaming if requested
	if *autoStart {
		log.Printf("Auto-start enabled. Launching %s pipeline...", snap.Media.DefaultMode)
		target := snap.Media.VideoFile
		if snap.Media.DefaultMode != "video" {
			target = ""
		}
		if err := mediaCore.Play(snap.Media.DefaultMode, target); err != nil {
			log.Printf("Warning: failed to auto-start pipeline: %v", err)
		}
	}

	// Graceful shutdown handling
	stopSignal := make(chan os.Signal, 1)
	signal.Notify(stopSignal, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("Server stopped: %v", err)
		}
	}()

	log.Printf("Controller is ready! Access Web-GUI at http://localhost:%d", snap.Server.HTTPPort)

	sig := <-stopSignal
	log.Printf("Received signal %v. Initiating graceful shutdown...", sig)

	_ = mediaCore.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	log.Println("booth-display controller terminated successfully.")
}
