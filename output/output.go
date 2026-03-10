package output

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"duplistream/stats"
)

// HeaderProvider provides stream headers for reconnecting outputs
type HeaderProvider interface {
	GetStreamHeader() (header []byte, session int)
}

type Output struct {
	Name         string
	URL          string
	Key          string
	AudioOnly    bool
	AudioBitrate string
	AudioCopy    bool

	headerProvider HeaderProvider
	lastSession    int

	stats      *stats.Stats
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	running    bool
	reconnects int
	lastError  string
	mu         sync.RWMutex
	cancel     context.CancelFunc
}

type OutputConfig struct {
	Name           string
	URL            string
	Key            string
	AudioOnly      bool
	AudioBitrate   string
	AudioCopy      bool
	HeaderProvider HeaderProvider
}

func New(cfg OutputConfig) *Output {
	bitrate := cfg.AudioBitrate
	if bitrate == "" {
		bitrate = "256k"
	}
	return &Output{
		Name:           cfg.Name,
		URL:            cfg.URL,
		Key:            cfg.Key,
		AudioOnly:      cfg.AudioOnly,
		AudioBitrate:   bitrate,
		AudioCopy:      cfg.AudioCopy,
		headerProvider: cfg.HeaderProvider,
		stats:          stats.New(),
	}
}

func (o *Output) Status() OutputStatus {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return OutputStatus{
		Name:       o.Name,
		Running:    o.running,
		AudioOnly:  o.AudioOnly,
		Reconnects: o.reconnects,
		Error:      o.lastError,
		Stats:      o.stats.Get(),
	}
}

type OutputStatus struct {
	Name       string      `json:"name"`
	Running    bool        `json:"running"`
	AudioOnly  bool        `json:"audio_only"`
	Reconnects int         `json:"reconnects"`
	Error      string      `json:"error,omitempty"`
	Stats      stats.Stats `json:"stats"`
}

// Run starts the output with automatic reconnection.
// It reads FLV data from inputChan and sends to the RTMP destination.
func (o *Output) Run(ctx context.Context, inputChan <-chan []byte) {
	backoff := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		5 * time.Second,
		10 * time.Second,
		30 * time.Second,
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := o.runOnce(ctx, inputChan)
		if ctx.Err() != nil {
			return
		}

		o.mu.Lock()
		o.reconnects++
		attempt := o.reconnects
		if err != nil {
			o.lastError = err.Error()
		}
		o.mu.Unlock()

		delay := backoff[min(attempt-1, len(backoff)-1)]
		log.Printf("[%s] Connection lost, reconnecting in %v... (attempt %d)", o.Name, delay, attempt)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (o *Output) runOnce(ctx context.Context, inputChan <-chan []byte) error {
	destURL := fmt.Sprintf("%s/%s", o.URL, o.Key)

	// Build FFmpeg args
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-f", "flv",
		"-i", "pipe:0",
		"-progress", "pipe:2",
	}

	if o.AudioOnly {
		// Audio-only output - must re-encode to strip video
		args = append(args,
			"-vn",
			"-c:a", "aac",
			"-b:a", o.AudioBitrate,
			"-ar", "44100",
		)
	} else if o.AudioCopy {
		// Pass through both video and audio without re-encoding
		args = append(args,
			"-c:v", "copy",
			"-c:a", "copy",
		)
	} else {
		// Copy video, re-encode audio to AAC
		args = append(args,
			"-c:v", "copy",
			"-c:a", "aac",
			"-b:a", o.AudioBitrate,
		)
	}

	args = append(args,
		"-f", "flv",
		"-flvflags", "no_duration_filesize",
		destURL,
	)

	cmdCtx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.cancel = cancel
	o.mu.Unlock()

	cmd := exec.CommandContext(cmdCtx, "ffmpeg", args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	o.mu.Lock()
	o.cmd = cmd
	o.stdin = stdin
	o.running = true
	o.lastError = ""
	o.stats.Reset()
	o.mu.Unlock()

	log.Printf("[%s] Connected to %s", o.Name, maskURL(destURL))

	// Parse progress output in background
	go o.stats.ParseProgress(stderr)

	// Check if we need to write the stream header (reconnecting to an existing stream)
	// Only write header if:
	// 1. We have a header provider
	// 2. We've connected before (lastSession > 0) - meaning this is a reconnect
	// 3. The stream session matches (we're reconnecting to the same stream)
	if o.headerProvider != nil && o.lastSession > 0 {
		header, session := o.headerProvider.GetStreamHeader()
		if header != nil && session == o.lastSession {
			// Same stream session - we're reconnecting mid-stream, need header
			// Drain any stale data from channel first
			drainCount := 0
			for {
				select {
				case <-inputChan:
					drainCount++
				default:
					goto drained
				}
			}
		drained:
			if drainCount > 0 {
				log.Printf("[%s] Drained %d stale packets", o.Name, drainCount)
			}

			// Write cached header to FFmpeg
			if _, err := stdin.Write(header); err != nil {
				cancel()
				cmd.Wait()
				return fmt.Errorf("write header: %w", err)
			}
			log.Printf("[%s] Wrote stream header (%d bytes)", o.Name, len(header))
		} else if session != o.lastSession {
			// Different stream session - new OBS connection, update session
			// Don't write header - we'll get fresh data with headers from channel
			o.lastSession = session
			log.Printf("[%s] New stream session %d", o.Name, session)
		}
	}

	// Track the session on first connection
	if o.headerProvider != nil && o.lastSession == 0 {
		_, session := o.headerProvider.GetStreamHeader()
		o.lastSession = session
	}

	// Write input data to FFmpeg stdin
	var writeErr error
	for {
		select {
		case <-cmdCtx.Done():
			goto cleanup
		case data, ok := <-inputChan:
			if !ok {
				goto cleanup
			}
			if _, err := stdin.Write(data); err != nil {
				writeErr = err
				goto cleanup
			}
		}
	}

cleanup:
	stdin.Close()
	cancel()
	cmd.Wait()

	o.mu.Lock()
	o.running = false
	o.cmd = nil
	o.stdin = nil
	o.mu.Unlock()

	if writeErr != nil {
		return writeErr
	}
	return nil
}

// Stop gracefully stops the output
func (o *Output) Stop() {
	o.mu.Lock()
	cancel := o.cancel
	o.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func maskURL(url string) string {
	// Find the last / and mask most of the key
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			key := url[i+1:]
			if len(key) > 8 {
				return url[:i+1] + key[:4] + "****"
			}
			break
		}
	}
	return url
}
