package input

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	listenAddr    string
	app           string
	expectedKey   string
	onConnected   func()
	onDisconnected func()

	cmd       *exec.Cmd
	stdout    io.ReadCloser
	connected bool
	startTime time.Time
	outputs   []chan []byte
	mu        sync.RWMutex

	// Stream header caching for output reconnection
	streamHeader  []byte
	streamSession int
	headerMu      sync.RWMutex
}

type Config struct {
	ListenAddr     string
	App            string
	ExpectedKey    string
	OnConnected    func()
	OnDisconnected func()
}

func NewManager(cfg Config) *Manager {
	return &Manager{
		listenAddr:     cfg.ListenAddr,
		app:            cfg.App,
		expectedKey:    cfg.ExpectedKey,
		onConnected:    cfg.OnConnected,
		onDisconnected: cfg.OnDisconnected,
	}
}

func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

func (m *Manager) Uptime() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.connected {
		return 0
	}
	return time.Since(m.startTime)
}

// GetStreamHeader returns the cached FLV header and current stream session ID.
// Outputs use this to get headers when reconnecting mid-stream.
func (m *Manager) GetStreamHeader() (header []byte, session int) {
	m.headerMu.RLock()
	defer m.headerMu.RUnlock()
	if m.streamHeader == nil {
		return nil, m.streamSession
	}
	// Return a copy to avoid races
	headerCopy := make([]byte, len(m.streamHeader))
	copy(headerCopy, m.streamHeader)
	return headerCopy, m.streamSession
}

// GetStreamSession returns the current stream session ID
func (m *Manager) GetStreamSession() int {
	m.headerMu.RLock()
	defer m.headerMu.RUnlock()
	return m.streamSession
}

// AddOutput registers an output channel to receive input data
func (m *Manager) AddOutput(ch chan []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outputs = append(m.outputs, ch)
}

// RemoveOutput unregisters an output channel
func (m *Manager) RemoveOutput(ch chan []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, c := range m.outputs {
		if c == ch {
			m.outputs = append(m.outputs[:i], m.outputs[i+1:]...)
			return
		}
	}
}

// Run starts listening for RTMP input and fans out to all outputs.
// It loops forever, restarting FFmpeg when the stream ends.
func (m *Manager) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Println("Waiting for OBS to connect...")
		err := m.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			log.Printf("Stream ended: %v", err)
		} else {
			log.Println("Stream ended")
		}

		// Brief pause before accepting new connection
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (m *Manager) runOnce(ctx context.Context) error {
	listenAddr := m.listenAddr
	if listenAddr == "" {
		listenAddr = "0.0.0.0:1935"
	} else if listenAddr[0] == ':' {
		listenAddr = "0.0.0.0" + listenAddr
	}

	inputURL := fmt.Sprintf("rtmp://%s/%s", listenAddr, m.app)

	// Input FFmpeg: listen for RTMP, output raw FLV to stdout
	args := []string{
		"-hide_banner",
		"-loglevel", "verbose",
		"-listen", "1",
		"-timeout", "30",
		"-i", inputURL,
		"-c", "copy",
		"-f", "flv",
		"pipe:1",
	}

	// Retry starting FFmpeg in case the port isn't released yet
	var cmd *exec.Cmd
	var stdout, stderr io.ReadCloser
	var err error

	for attempt := 1; attempt <= 10; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		cmd = exec.CommandContext(ctx, "ffmpeg", args...)

		stdout, err = cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("stdout pipe: %w", err)
		}

		stderr, err = cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("stderr pipe: %w", err)
		}

		if err = cmd.Start(); err != nil {
			log.Printf("Failed to start listener (attempt %d/10): %v", attempt, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Started successfully
		break
	}

	if err != nil {
		return fmt.Errorf("start ffmpeg after retries: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.stdout = stdout
	m.mu.Unlock()

	// Monitor stderr for connection and validate stream key
	connected := make(chan bool, 1)
	invalidKey := make(chan string, 1)

	go m.monitorStderr(stderr, connected, invalidKey)

	// Wait for connection or timeout/error
	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
		return ctx.Err()
	case key := <-invalidKey:
		log.Printf("Invalid stream key rejected: %s****", key[:min(4, len(key))])
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
		return fmt.Errorf("invalid stream key")
	case <-connected:
		// Stream connected, proceed
	}

	m.mu.Lock()
	m.connected = true
	m.startTime = time.Now()
	m.mu.Unlock()

	// Increment stream session and clear old header
	m.headerMu.Lock()
	m.streamSession++
	m.streamHeader = nil
	m.headerMu.Unlock()

	if m.onConnected != nil {
		m.onConnected()
	}

	log.Println("OBS connected! Streaming to all outputs...")

	// Fan out stdout to all registered outputs
	err = m.fanout(ctx, stdout)

	m.mu.Lock()
	m.connected = false
	m.cmd = nil
	m.stdout = nil
	m.mu.Unlock()

	if m.onDisconnected != nil {
		m.onDisconnected()
	}

	cmd.Wait()
	return err
}

func (m *Manager) monitorStderr(r io.Reader, connected chan<- bool, invalidKey chan<- string) {
	scanner := bufio.NewScanner(r)
	notified := false

	for scanner.Scan() {
		line := scanner.Text()

		// Check for stream key in the connection
		// FFmpeg logs: "Unexpected stream <key>, expecting <app>"
		if strings.Contains(line, "Unexpected stream") {
			// Extract the key from the message
			// Format: "Unexpected stream <key>, expecting <app>"
			if start := strings.Index(line, "Unexpected stream "); start >= 0 {
				rest := line[start+18:]
				if end := strings.Index(rest, ","); end > 0 {
					receivedKey := rest[:end]
					if m.expectedKey != "" && receivedKey != m.expectedKey {
						select {
						case invalidKey <- receivedKey:
						default:
						}
						continue
					}
				}
			}
		}

		// Detect actual connection: "Input #0, flv, from..."
		if !notified && strings.HasPrefix(line, "Input #0") {
			notified = true
			select {
			case connected <- true:
			default:
			}
		}

		// Filter noise, log important messages
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "frame=") || strings.HasPrefix(line, "size=") {
			continue
		}
		log.Printf("[input] %s", line)
	}
}

func (m *Manager) fanout(ctx context.Context, r io.Reader) error {
	buf := make([]byte, 32*1024) // 32KB buffer

	// Cache initial data as header for reconnecting outputs
	// FLV header + metadata + first keyframe is typically under 1MB
	const maxHeaderSize = 1024 * 1024 // 1MB
	var headerBuf []byte
	headerComplete := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			// Cache header data until we have enough
			if !headerComplete {
				headerBuf = append(headerBuf, data...)
				if len(headerBuf) >= maxHeaderSize {
					headerComplete = true
					m.headerMu.Lock()
					m.streamHeader = make([]byte, len(headerBuf))
					copy(m.streamHeader, headerBuf)
					m.headerMu.Unlock()
					headerBuf = nil // Free memory
					log.Printf("[input] Stream header cached (%d bytes)", len(m.streamHeader))
				}
			}

			m.mu.RLock()
			outputs := make([]chan []byte, len(m.outputs))
			copy(outputs, m.outputs)
			m.mu.RUnlock()

			for _, ch := range outputs {
				select {
				case ch <- data:
				default:
					// Output channel full, drop data for this output
					// This prevents one slow output from blocking others
				}
			}
		}
	}
}

// Stop gracefully stops the input manager
func (m *Manager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
	}
}
