package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"duplistream/config"
	"duplistream/input"
	"duplistream/output"
	"duplistream/web"
)

type Duplistream struct {
	cfg        *config.Config
	configPath string
	ctx        context.Context
	cancel     context.CancelFunc

	inputMgr *input.Manager
	outputs  map[string]*output.Output
	outputMu sync.RWMutex
}

var Version = "dev"

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Duplistream %s\n", Version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	d := &Duplistream{
		cfg:        cfg,
		configPath: *configPath,
		ctx:        ctx,
		cancel:     cancel,
		outputs:    make(map[string]*output.Output),
	}

	// Initialize input manager
	d.inputMgr = input.NewManager(input.Config{
		ListenAddr:  cfg.Server.Listen,
		App:         cfg.Server.App,
		ExpectedKey: cfg.Server.StreamKey,
	})

	// Initialize outputs
	d.initOutputs()

	// Start web server
	webServer := web.NewServer(cfg.Server.StatusPort, d)
	go func() {
		log.Printf("Web dashboard: http://localhost%s", cfg.Server.StatusPort)
		if err := webServer.Start(); err != nil {
			log.Printf("Web server error: %v", err)
		}
	}()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for sig := range sigChan {
			switch sig {
			case syscall.SIGHUP:
				log.Println("SIGHUP received, reloading config...")
				d.reloadConfig()
			case syscall.SIGINT, syscall.SIGTERM:
				log.Println("Shutdown signal received")
				cancel()
				return
			}
		}
	}()

	// Print startup info
	d.printStartupInfo()

	// Run duplistream
	d.run()
}

func (d *Duplistream) initOutputs() {
	d.outputMu.Lock()
	defer d.outputMu.Unlock()

	for name, outCfg := range d.cfg.Outputs {
		if !outCfg.Enabled || outCfg.Key == "" {
			continue
		}

		out := output.New(output.OutputConfig{
			Name:         name,
			URL:          outCfg.URL,
			Key:          outCfg.Key,
			AudioOnly:    outCfg.AudioOnly,
			AudioBitrate: "256k",
		})
		d.outputs[name] = out
	}
}

func (d *Duplistream) printStartupInfo() {
	log.Printf("==============================================")
	log.Printf("Duplistream started")
	log.Printf("==============================================")
	log.Printf("OBS Settings:")
	log.Printf("  Server: rtmp://<this-server-ip>%s/%s", d.cfg.Server.Listen, d.cfg.Server.App)
	if d.cfg.Server.StreamKey != "" {
		log.Printf("  Stream Key: %s", maskStreamKey(d.cfg.Server.StreamKey))
	} else {
		log.Printf("  Stream Key: (any)")
	}
	log.Printf("==============================================")
	log.Printf("Outputs configured:")

	d.outputMu.RLock()
	for name, outCfg := range d.cfg.Outputs {
		status := "disabled"
		if outCfg.Enabled && outCfg.Key != "" {
			if outCfg.AudioOnly {
				status = "enabled (audio-only)"
			} else {
				status = "enabled (video+audio)"
			}
		} else if outCfg.Enabled && outCfg.Key == "" {
			status = "enabled but NO KEY SET"
		}
		log.Printf("  - %s: %s", name, status)
	}
	d.outputMu.RUnlock()

	log.Printf("==============================================")
}

func (d *Duplistream) run() {
	// Create channels for each output and start output goroutines
	d.outputMu.RLock()
	outputChans := make(map[string]chan []byte)
	for name, out := range d.outputs {
		ch := make(chan []byte, 100) // Buffer to handle brief slowdowns
		outputChans[name] = ch
		d.inputMgr.AddOutput(ch)

		go func(name string, out *output.Output, ch chan []byte) {
			out.Run(d.ctx, ch)
		}(name, out, ch)

		log.Printf("[%s] Output ready", name)
	}
	d.outputMu.RUnlock()

	if len(outputChans) == 0 {
		log.Println("Warning: No outputs configured. Waiting for config reload (SIGHUP)...")
	}

	// Run input manager (blocks until shutdown)
	d.inputMgr.Run(d.ctx)

	// Cleanup
	log.Println("Shutting down...")
	for _, ch := range outputChans {
		close(ch)
	}
}

func (d *Duplistream) reloadConfig() {
	newCfg, err := config.Load(d.configPath)
	if err != nil {
		log.Printf("Failed to reload config: %v", err)
		return
	}

	d.outputMu.Lock()
	defer d.outputMu.Unlock()

	// Find outputs to add/remove/update
	currentNames := make(map[string]bool)
	for name := range d.outputs {
		currentNames[name] = true
	}

	newNames := make(map[string]bool)
	for name, outCfg := range newCfg.Outputs {
		if outCfg.Enabled && outCfg.Key != "" {
			newNames[name] = true
		}
	}

	// Stop removed outputs
	for name := range currentNames {
		if !newNames[name] {
			log.Printf("[%s] Stopping (removed from config)", name)
			d.outputs[name].Stop()
			delete(d.outputs, name)
		}
	}

	// Add new outputs
	for name, outCfg := range newCfg.Outputs {
		if !outCfg.Enabled || outCfg.Key == "" {
			continue
		}
		if !currentNames[name] {
			log.Printf("[%s] Adding new output", name)
			out := output.New(output.OutputConfig{
				Name:         name,
				URL:          outCfg.URL,
				Key:          outCfg.Key,
				AudioOnly:    outCfg.AudioOnly,
				AudioBitrate: "256k",
			})
			d.outputs[name] = out

			ch := make(chan []byte, 100)
			d.inputMgr.AddOutput(ch)
			go out.Run(d.ctx, ch)
		}
	}

	// Update stream key if changed
	if newCfg.Server.StreamKey != d.cfg.Server.StreamKey {
		log.Printf("Stream key updated")
	}

	d.cfg = newCfg
	log.Printf("Config reloaded successfully")
}

// StatusProvider interface implementation for web server

func (d *Duplistream) IsInputConnected() bool {
	return d.inputMgr.IsConnected()
}

func (d *Duplistream) Uptime() time.Duration {
	return d.inputMgr.Uptime()
}

func (d *Duplistream) Outputs() map[string]*output.Output {
	d.outputMu.RLock()
	defer d.outputMu.RUnlock()

	// Return a copy to avoid race conditions
	result := make(map[string]*output.Output)
	for k, v := range d.outputs {
		result[k] = v
	}
	return result
}

func (d *Duplistream) HealthStatus() string {
	if !d.inputMgr.IsConnected() {
		return "down"
	}

	d.outputMu.RLock()
	defer d.outputMu.RUnlock()

	allRunning := true
	for _, out := range d.outputs {
		if !out.Status().Running {
			allRunning = false
			break
		}
	}

	if allRunning {
		return "healthy"
	}
	return "degraded"
}

func maskStreamKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + "****"
}
