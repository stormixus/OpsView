//go:build windows

package main

import (
	"fmt"
	"log"
	"sync"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
	"golang.org/x/sys/windows"
)

// --- Direct3D 11 / DXGI COM interfaces via syscall ---

var (
	d3d11              = windows.NewLazySystemDLL("d3d11.dll")
	procCreateDevice   = d3d11.NewProc("D3D11CreateDevice")
	dxgi               = windows.NewLazySystemDLL("dxgi.dll")
)

// GUIDs
var (
	IID_IDXGIDevice  = windows.GUID{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	IID_IDXGIAdapter = windows.GUID{0x2411e7e1, 0x12ac, 0x4ccf, [8]byte{0xbd, 0x14, 0x97, 0x98, 0xe8, 0x53, 0x4d, 0xc0}}
	IID_IDXGIOutput1 = windows.GUID{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
)

const (
	DXGI_FORMAT_B8G8R8A8_UNORM     = 87
	D3D11_SDK_VERSION              = 7
	D3D_DRIVER_TYPE_HARDWARE       = 1
	D3D11_CREATE_DEVICE_BGRA       = 0x20
	D3D11_USAGE_STAGING            = 3
	D3D11_CPU_ACCESS_READ          = 0x20000
	DXGI_MAP_READ                  = 1
	DXGI_ERROR_WAIT_TIMEOUT        = 0x887A0027
	DXGI_ERROR_ACCESS_LOST         = 0x887A0026
	DXGI_ERROR_ACCESS_DENIED       = 0x887A002B
)

// DXGICapturer captures the screen using DXGI Desktop Duplication.
type DXGICapturer struct {
	cfg      AgentConfig
	device   uintptr // *ID3D11Device
	ctx      uintptr // *ID3D11DeviceContext
	dup      uintptr // *IDXGIOutputDuplication
	staging  uintptr // *ID3D11Texture2D
	width    int
	height   int
	tileSize int
	encoder  *zstd.Encoder
	prevFrame []byte // previous full frame for delta detection
	mu       sync.Mutex
}

func NewCapturer(cfg AgentConfig) (Capturer, error) {
	c := &DXGICapturer{
		cfg:      cfg,
		tileSize: cfg.TileSize,
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return nil, fmt.Errorf("zstd encoder: %w", err)
	}
	c.encoder = enc

	if err := c.init(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *DXGICapturer) init() error {
	// Create D3D11 device
	var device, ctx uintptr
	hr, _, _ := procCreateDevice.Call(
		0,                        // pAdapter (NULL = default)
		D3D_DRIVER_TYPE_HARDWARE, // DriverType
		0,                        // Software
		D3D11_CREATE_DEVICE_BGRA, // Flags
		0,                        // pFeatureLevels (NULL = default)
		0,                        // FeatureLevels count
		D3D11_SDK_VERSION,        // SDKVersion
		uintptr(unsafe.Pointer(&device)),
		0, // pFeatureLevel (out, don't care)
		uintptr(unsafe.Pointer(&ctx)),
	)
	if hr != 0 {
		return fmt.Errorf("D3D11CreateDevice failed: 0x%X", hr)
	}
	c.device = device
	c.ctx = ctx

	// Get IDXGIDevice → IDXGIAdapter → IDXGIOutput → IDXGIOutput1
	var dxgiDevice uintptr
	hr = comQueryInterface(device, &IID_IDXGIDevice, &dxgiDevice)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IDXGIDevice: 0x%X", hr)
	}
	defer comRelease(dxgiDevice)

	var adapter uintptr
	hr = comCall(dxgiDevice, 7, uintptr(unsafe.Pointer(&adapter))) // IDXGIDevice::GetParent/GetAdapter at vtable[7]
	if hr != 0 {
		return fmt.Errorf("GetAdapter: 0x%X", hr)
	}
	defer comRelease(adapter)

	var output uintptr
	hr = comCall(adapter, 7, 0, uintptr(unsafe.Pointer(&output))) // IDXGIAdapter::EnumOutputs(0, &output)
	if hr != 0 {
		return fmt.Errorf("EnumOutputs: 0x%X", hr)
	}
	defer comRelease(output)

	var output1 uintptr
	hr = comQueryInterface(output, &IID_IDXGIOutput1, &output1)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IDXGIOutput1: 0x%X", hr)
	}
	defer comRelease(output1)

	// DuplicateOutput
	var dup uintptr
	hr = comCall(output1, 22, uintptr(device), uintptr(unsafe.Pointer(&dup))) // IDXGIOutput1::DuplicateOutput
	if hr != 0 {
		return fmt.Errorf("DuplicateOutput: 0x%X", hr)
	}
	c.dup = dup

	// Get output description for resolution
	c.width = 1920
	c.height = 1080
	if c.cfg.Profile == 720 {
		c.width = 1280
		c.height = 720
	}

	log.Printf("[capturer] initialized DXGI %dx%d tile=%d", c.width, c.height, c.tileSize)
	return nil
}

func (c *DXGICapturer) CaptureFrame() ([]proto.Tile, int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dup == 0 {
		return nil, 0, 0, fmt.Errorf("duplication not initialized")
	}

	// AcquireNextFrame with 100ms timeout
	var frameInfo [6]uint64 // DXGI_OUTDUPL_FRAME_INFO (48 bytes)
	var resource uintptr
	hr := comCall(c.dup, 8, // IDXGIOutputDuplication::AcquireNextFrame
		100, // TimeoutInMilliseconds
		uintptr(unsafe.Pointer(&frameInfo[0])),
		uintptr(unsafe.Pointer(&resource)),
	)
	if hr == DXGI_ERROR_WAIT_TIMEOUT {
		return nil, c.width, c.height, nil // No changes
	}
	if hr == DXGI_ERROR_ACCESS_LOST || hr == DXGI_ERROR_ACCESS_DENIED {
		return nil, 0, 0, fmt.Errorf("access lost (0x%X), need reinit", hr)
	}
	if hr != 0 {
		return nil, 0, 0, fmt.Errorf("AcquireNextFrame: 0x%X", hr)
	}
	defer comCall(c.dup, 11) // ReleaseFrame
	defer comRelease(resource)

	// Get dirty rects
	dirtyRects := c.getDirtyRects()

	// Map the frame texture and extract changed tiles
	tiles := c.extractTiles(resource, dirtyRects)

	return tiles, c.width, c.height, nil
}

type rect struct {
	left, top, right, bottom int
}

func (c *DXGICapturer) getDirtyRects() []rect {
	buf := make([]byte, 4096)
	var needed uint32
	hr := comCall(c.dup, 9, // GetFrameDirtyRects
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&needed)),
	)
	if hr != 0 || needed == 0 {
		// Fall back to full screen as dirty
		return []rect{{0, 0, c.width, c.height}}
	}

	count := int(needed) / 16 // sizeof(RECT) = 16
	rects := make([]rect, count)
	for i := 0; i < count; i++ {
		off := i * 16
		rects[i] = rect{
			left:   int(*(*int32)(unsafe.Pointer(&buf[off]))),
			top:    int(*(*int32)(unsafe.Pointer(&buf[off+4]))),
			right:  int(*(*int32)(unsafe.Pointer(&buf[off+8]))),
			bottom: int(*(*int32)(unsafe.Pointer(&buf[off+12]))),
		}
	}
	return rects
}

func (c *DXGICapturer) extractTiles(resource uintptr, dirtyRects []rect) []proto.Tile {
	// Build set of dirty tile coordinates
	tileSet := make(map[[2]int]bool)
	ts := c.tileSize
	for _, r := range dirtyRects {
		tx0 := r.left / ts
		tx1 := (r.right - 1) / ts
		ty0 := r.top / ts
		ty1 := (r.bottom - 1) / ts
		for ty := ty0; ty <= ty1; ty++ {
			for tx := tx0; tx <= tx1; tx++ {
				tileSet[[2]int{tx, ty}] = true
			}
		}
	}

	// TODO: Map texture and copy pixel data for each dirty tile
	// This is a simplified stub - actual implementation would:
	// 1. QueryInterface resource for ID3D11Texture2D
	// 2. CopyResource to staging texture
	// 3. Map staging texture
	// 4. For each dirty tile, extract BGRA pixels and compress with zstd
	var tiles []proto.Tile
	for coord := range tileSet {
		tx, ty := coord[0], coord[1]
		// Placeholder: in production, actual pixel data would be extracted here
		tileW := ts
		tileH := ts
		if (tx+1)*ts > c.width {
			tileW = c.width - tx*ts
		}
		if (ty+1)*ts > c.height {
			tileH = c.height - ty*ts
		}

		// Dummy BGRA data for the tile
		raw := make([]byte, tileW*tileH*4)
		compressed := c.encoder.EncodeAll(raw, nil)

		tiles = append(tiles, proto.Tile{
			TX:      uint16(tx),
			TY:      uint16(ty),
			Codec:   proto.CodecZstdRawBGRA,
			DataLen: uint32(len(compressed)),
			Data:    compressed,
		})
	}

	return tiles
}

func (c *DXGICapturer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dup != 0 {
		comRelease(c.dup)
		c.dup = 0
	}
	if c.staging != 0 {
		comRelease(c.staging)
		c.staging = 0
	}
	if c.ctx != 0 {
		comRelease(c.ctx)
		c.ctx = 0
	}
	if c.device != 0 {
		comRelease(c.device)
		c.device = 0
	}
	if c.encoder != nil {
		c.encoder.Close()
	}
	log.Println("[capturer] closed")
}

// --- COM helper functions ---

func comQueryInterface(obj uintptr, iid *windows.GUID, out *uintptr) uintptr {
	vtable := *(*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	r, _, _ := windows.SyscallN(vtable[0], obj, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out)))
	return uintptr(r)
}

func comRelease(obj uintptr) {
	if obj == 0 {
		return
	}
	vtable := *(*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	windows.SyscallN(vtable[2], obj)
}

func comCall(obj uintptr, method int, args ...uintptr) uintptr {
	vtable := *(*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	allArgs := append([]uintptr{obj}, args...)
	r, _, _ := windows.SyscallN(vtable[method], allArgs...)
	return uintptr(r)
}
