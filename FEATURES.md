# Features

Detailed documentation of Duplistream features.

## Architecture

```
┌─────────┐      ┌──────────────────┐      ┌─────────────────┐
│   OBS   │─────▶│  Input FFmpeg    │─────▶│  Go Fanout      │
└─────────┘      │  (RTMP listener) │      │  (in-memory)    │
                 └──────────────────┘      └────────┬────────┘
                                                    │
                        ┌───────────────────────────┼───────────────────────────┐
                        │                           │                           │
                        ▼                           ▼                           ▼
               ┌─────────────────┐         ┌─────────────────┐         ┌─────────────────┐
               │ Output FFmpeg   │         │ Output FFmpeg   │         │ Output FFmpeg   │
               │ (YouTube)       │         │ (Facebook)      │         │ (Mixcloud)      │
               └─────────────────┘         └─────────────────┘         └─────────────────┘
```

Each output runs as an independent FFmpeg process. This means:
- One slow or failing output won't affect others
- Outputs can reconnect independently
- Per-output statistics tracking

## Output Isolation

Unlike simple tee-based approaches, each output destination runs in its own FFmpeg process.

**Benefits:**
- If Facebook's servers are slow, YouTube and Mixcloud continue unaffected
- If one platform rejects your stream (wrong key, account issue), others keep working
- Each output can have different settings (audio-only, different bitrates in future)

**How it works:**
1. Input FFmpeg receives the RTMP stream and outputs raw FLV to stdout
2. Go reads this data and copies it to each output's channel
3. Each output FFmpeg reads from its channel and sends to the destination

## Auto-Reconnect

When an output connection drops, it automatically reconnects with exponential backoff:

| Attempt | Delay |
|---------|-------|
| 1 | 1 second |
| 2 | 2 seconds |
| 3 | 5 seconds |
| 4 | 10 seconds |
| 5+ | 30 seconds |

The backoff resets when a connection succeeds.

**Dashboard shows:**
- Current connection status (Live/Offline/Reconnecting)
- Number of reconnection attempts
- Last error message

## Stream Statistics

Each output tracks real-time statistics:

| Stat | Description |
|------|-------------|
| Frames | Total video frames sent |
| Bitrate | Current output bitrate |
| Duration | Time streaming to this output |
| Size | Total bytes sent |

Statistics are available via:
- Web dashboard at `/`
- JSON API at `/status`

## Web Dashboard

A read-only web dashboard showing live status:

- **Main status indicator**: Green (all healthy), Yellow (degraded), Red (down)
- **Input status**: Whether OBS is connected, stream uptime
- **Per-output cards**: Connection status, stats, errors
- **Auto-refresh**: Updates every 2 seconds

Access at `http://localhost:9090/` (or your configured status_port).

## Hot Reload (SIGHUP)

Update your configuration without dropping the stream:

```bash
# Edit config.yaml, then:
kill -HUP $(pgrep duplistream)
```

**What can be hot-reloaded:**
- Enable/disable outputs
- Add new outputs
- Remove outputs
- Change stream keys
- Update stream key validation

**What requires restart:**
- Changing listen port
- Changing app name

**Example workflow:**
1. Start streaming to Facebook and Mixcloud
2. Decide you also want YouTube
3. Edit config.yaml, enable YouTube, add key
4. Send SIGHUP
5. YouTube output starts without interrupting existing streams

## Stream Key Validation

Optional security to reject unauthorized streams.

**Config:**
```yaml
server:
  stream_key: "my-secret-key"
```

**OBS setup:**
- Server: `rtmp://your-server:1935/live`
- Stream Key: `my-secret-key`

If someone tries to stream with the wrong key, the connection is rejected and logged.

**Leave empty to accept any stream key:**
```yaml
server:
  stream_key: ""
```

## Health Checks

`GET /health` returns the system status:

```json
{
  "status": "healthy",
  "input_connected": true,
  "outputs": {
    "youtube": {"running": true, "error": ""},
    "facebook": {"running": true, "error": ""}
  }
}
```

**Status values:**
| Status | Meaning |
|--------|---------|
| `healthy` | Input connected, all outputs running |
| `degraded` | Input connected, some outputs failing |
| `down` | Input not connected |

**HTTP status codes:**
- `200 OK` for healthy or degraded
- `503 Service Unavailable` for down

Useful for:
- Load balancer health checks
- Monitoring systems (Prometheus, Datadog, etc.)
- Alerting

## Audio-Only Outputs

Some platforms (like Mixlr) only accept audio. Configure with:

```yaml
mixlr:
  enabled: true
  url: "rtmp://rtmp.mixlr.com/broadcast"
  key: "your-key"
  audio_only: true
```

Audio-only outputs:
- Strip video stream
- Transcode to AAC at 256kbps
- Resample to 44.1kHz (standard for audio platforms)

## Configuration Reference

### Server Section

```yaml
server:
  listen: ":1935"        # RTMP listen address
  app: "live"            # RTMP application name
  stream_key: ""         # Required stream key (empty = any)
  status_port: ":9090"   # HTTP dashboard/API port
```

### Output Section

```yaml
outputs:
  name:                  # Identifier (shown in logs/dashboard)
    enabled: true        # Whether to stream to this output
    url: "rtmp://..."    # RTMP server URL (without key)
    key: "stream-key"    # Stream key (supports ${ENV_VAR})
    audio_only: false    # Strip video, send audio only
```

### Environment Variables

Stream keys can reference environment variables:

```yaml
key: "${YOUTUBE_STREAM_KEY}"
```

Set before running:
```bash
export YOUTUBE_STREAM_KEY="xxxx-xxxx-xxxx"
./rtmp-restream
```

## Common RTMP URLs

| Platform | URL |
|----------|-----|
| YouTube | `rtmp://a.rtmp.youtube.com/live2` |
| Facebook | `rtmps://live-api-s.facebook.com:443/rtmp/` |
| Twitch | `rtmp://live.twitch.tv/app` |
| Mixcloud | `rtmp://rtmp.mixcloud.com/broadcast` |
| Mixlr | `rtmp://rtmp.mixlr.com/broadcast` |

## Logs

Log output includes:
- `[input]` - Input FFmpeg messages
- `[youtube]` - Per-output messages (connection, errors)
- Startup info with configured outputs
- Connection/disconnection events

Example:
```
2024/01/15 20:30:00 Duplistream started
2024/01/15 20:30:00 Web dashboard: http://localhost:9090
2024/01/15 20:30:00 [youtube] Output ready
2024/01/15 20:30:00 [facebook] Output ready
2024/01/15 20:30:00 Waiting for OBS to connect...
2024/01/15 20:30:15 OBS connected! Streaming to all outputs...
2024/01/15 20:30:16 [youtube] Connected to rtmp://a.rtmp.youtube.com/live2/xxxx****
2024/01/15 20:30:16 [facebook] Connected to rtmps://live-api-s.facebook.com:443/rtmp//FB-1****
```
