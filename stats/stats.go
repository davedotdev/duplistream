package stats

import (
	"bufio"
	"io"
	"strconv"
	"strings"
	"sync"
)

type Stats struct {
	Frames   int64  `json:"frames"`
	FPS      float64 `json:"fps"`
	Bitrate  string `json:"bitrate"`
	Size     int64  `json:"size_bytes"`
	Duration string `json:"duration"`
	Speed    string `json:"speed"`
	mu       sync.RWMutex
}

func New() *Stats {
	return &Stats{}
}

func (s *Stats) Get() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		Frames:   s.Frames,
		FPS:      s.FPS,
		Bitrate:  s.Bitrate,
		Size:     s.Size,
		Duration: s.Duration,
		Speed:    s.Speed,
	}
}

func (s *Stats) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Frames = 0
	s.FPS = 0
	s.Bitrate = ""
	s.Size = 0
	s.Duration = ""
	s.Speed = ""
}

// ParseProgress reads FFmpeg progress output and updates stats.
// FFmpeg outputs progress like:
//
//	frame=1234
//	fps=30.0
//	bitrate=2500.0kbits/s
//	total_size=12345678
//	out_time=00:05:23.456789
//	speed=1.0x
//	progress=continue
func (s *Stats) ParseProgress(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		s.parseLine(line)
	}
}

func (s *Stats) parseLine(line string) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return
	}

	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])

	s.mu.Lock()
	defer s.mu.Unlock()

	switch key {
	case "frame":
		if v, err := strconv.ParseInt(value, 10, 64); err == nil {
			s.Frames = v
		}
	case "fps":
		if v, err := strconv.ParseFloat(value, 64); err == nil {
			s.FPS = v
		}
	case "bitrate":
		s.Bitrate = value
	case "total_size":
		if v, err := strconv.ParseInt(value, 10, 64); err == nil {
			s.Size = v
		}
	case "out_time":
		// Format: 00:05:23.456789 -> 00:05:23
		if idx := strings.Index(value, "."); idx > 0 {
			value = value[:idx]
		}
		s.Duration = value
	case "speed":
		s.Speed = value
	}
}
