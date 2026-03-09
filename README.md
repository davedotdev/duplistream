# Duplistream

A lightweight, self-hosted tool that duplicates your stream to multiple platforms simultaneously. Stream once from OBS, go live everywhere.

Built for DJs, musicians, and streamers who want to go live on multiple platforms without paying for cloud restreaming services.

## Features

- **Multi-platform streaming** - Stream to YouTube, Facebook, Mixcloud, Mixlr, Twitch, and any RTMP destination
- **Output isolation** - Each platform runs in its own process; one failing won't take down the others
- **Auto-reconnect** - Outputs automatically reconnect with exponential backoff if they drop
- **Live dashboard** - Web UI showing real-time status of all outputs
- **Hot reload** - Update config without restarting (add/remove outputs on the fly)
- **Stream key validation** - Optional security to reject unauthorized streams
- **Audio-only support** - Send audio-only streams to platforms like Mixlr

See [FEATURES.md](FEATURES.md) for detailed documentation.

## Quick Start

### Prerequisites

- FFmpeg installed and in PATH
- Go 1.21+ (only if building from source)

```bash
# macOS
brew install ffmpeg

# Ubuntu/Debian
sudo apt install ffmpeg

# Windows (chocolatey)
choco install ffmpeg

# Windows (manual)
# Download from https://ffmpeg.org/download.html and add to PATH
```

### Installation

**Download a pre-built binary** from the [Releases](https://github.com/yourusername/duplistream/releases) page, or build from source:

```bash
git clone https://github.com/yourusername/duplistream.git
cd duplistream
go build -o duplistream
```

#### Building for All Platforms

To build binaries for all supported platforms:

```bash
./build.sh v1.0.0
```

This creates binaries in the `dist/` directory:

```
dist/
├── duplistream-darwin-amd64      # macOS Intel
├── duplistream-darwin-arm64      # macOS Apple Silicon
├── duplistream-linux-amd64       # Linux x64
├── duplistream-linux-arm64       # Linux ARM64/Raspberry Pi
├── duplistream-windows-amd64.exe # Windows x64
└── duplistream-windows-arm64.exe # Windows ARM64
```

Check version with:

```bash
./duplistream -version
```

### Configuration

Copy the example config and add your stream keys:

```bash
cp config.yaml.example config.yaml
```

Edit `config.yaml`:

```yaml
server:
  listen: ":1935"
  app: "live"
  stream_key: ""  # Optional: require this key from OBS
  status_port: ":9090"

outputs:
  youtube:
    enabled: true
    url: "rtmp://a.rtmp.youtube.com/live2"
    key: "your-youtube-stream-key"
    audio_only: false

  facebook:
    enabled: true
    url: "rtmps://live-api-s.facebook.com:443/rtmp/"
    key: "your-facebook-stream-key"
    audio_only: false

  mixcloud:
    enabled: true
    url: "rtmp://rtmp.mixcloud.com/broadcast"
    key: "your-mixcloud-stream-key"
    audio_only: false
```

You can also use environment variables:

```yaml
key: "${YOUTUBE_STREAM_KEY}"
```

### Run

```bash
./duplistream -config config.yaml
```

### OBS Setup

1. Open OBS Settings → Stream
2. Set **Service** to "Custom..."
3. Set **Server** to `rtmp://your-server-ip:1935/live`
4. Set **Stream Key** to anything (or the key from your config if you set one)
5. Click "Start Streaming"

### Dashboard

Open `http://localhost:9090` in your browser to see the live dashboard with status of all outputs.

## Usage

### Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Web dashboard |
| `GET /status` | JSON status of all outputs |
| `GET /health` | Health check (healthy/degraded/down) |

### Hot Reload

Update `config.yaml` and reload without restarting:

```bash
kill -HUP $(pgrep duplistream)
```

### Running as a Service (systemd)

Create `/etc/systemd/system/duplistream.service`:

```ini
[Unit]
Description=Duplistream
After=network.target

[Service]
Type=simple
User=youruser
WorkingDirectory=/path/to/duplistream
ExecStart=/path/to/duplistream -config config.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable duplistream
sudo systemctl start duplistream

# Reload config
sudo systemctl reload duplistream
```

## Platform-Specific Notes

### Facebook

After starting your stream, you must manually click "Go Live" in Facebook's Live Producer interface. Facebook holds streams in preview mode until you do this.

### Mixcloud

Mixcloud accepts video streams but only broadcasts audio. No special config needed.

### YouTube

You may need to enable live streaming on your YouTube account first (can take 24 hours to activate).

## Troubleshooting

**"no outputs configured"**
All outputs are either disabled or missing stream keys. Check your config.

**"executable file not found in $PATH"**
FFmpeg isn't installed. Install it with your package manager.

**One output keeps reconnecting**
Check that platform's stream key is correct. The dashboard will show the error message.

**OBS says "Failed to connect"**
- Verify duplistream is running
- Check you're using the correct port (default: 1935)
- If using `stream_key` in config, make sure OBS is using the same key

## License

Apache 2.0 - See [LICENSE](LICENSE) for details.

Attribution must be preserved for all contributors. See [NOTICE](NOTICE) for contributor list.

## Contributing

PRs welcome! Please open an issue first to discuss major changes.
