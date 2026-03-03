# OVP (OpsView Protocol) v1

Binary protocol over WebSocket for screen tile delta streaming.

## Wire Format

All messages: `Header (12 bytes) + Payload (variable)`

### Header

```
Offset  Size  Field        Value
0       4     magic        0x4F565031 ('OVP1')
4       2     version      1
6       2     type         MessageType enum
8       4     payload_len  Payload size in bytes
```

All integers are **little-endian**.

### Message Types

| Type | Name | Direction | Payload |
|------|------|-----------|---------|
| 1 | HELLO | client→relay | JSON |
| 2 | AUTH | client→relay | JSON |
| 3 | FRAME_DELTA | agent→relay→viewer | Binary |
| 4 | FULL_FRAME | agent→relay→viewer | Binary (optional) |
| 5 | CONTROL | viewer→relay→agent | JSON (optional) |
| 6 | HEARTBEAT | bidirectional | Empty |
| 7 | ERROR | relay→client | JSON |

### HELLO (JSON)

```json
{
  "role": "publisher|watcher",
  "client": "opsview-agent|opsview-viewer|opsview-web",
  "client_version": "0.1.0",
  "supports": ["zstd"],
  "want_profile": "1080|720|null"
}
```

### AUTH (JSON)

```json
{ "token": "..." }
```

### FRAME_DELTA (Binary)

```
Offset  Size  Field
0       4     seq          Sequence number
4       8     ts_ms        Timestamp (unix ms)
12      2     profile      1080 or 720
14      2     width        Screen width
16      2     height       Screen height
18      2     tile_size    128
20      2     tile_count   Number of tiles following

-- Repeated tile_count times: --
0       2     tx           Tile X index
2       2     ty           Tile Y index
4       2     codec        1 = zstd_raw_bgra
6       4     data_len     Compressed data length
10      N     data         Compressed tile pixels
```

### Tile Codec

`1 = zstd_raw_bgra`: BGRA 8-bit pixels, zstd compressed (level 1-3).

Edge tiles (right/bottom border) may be smaller than tile_size.
Actual pixel dimensions: `min(tile_size, width - tx*tile_size)` x `min(tile_size, height - ty*tile_size)`.

### Connection Handshake

```
Client                    Relay
  |--- HELLO (WS bin) --->|
  |--- AUTH  (WS bin) --->|
  |                        | (validate)
  |<-- ERROR (if fail) ---|  (close)
  |                        |
  |<-- FRAME_DELTA -------|  (streaming)
  |<-- HEARTBEAT ---------|  (keepalive)
```
