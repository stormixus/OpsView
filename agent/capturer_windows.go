//go:build windows

package main

import (
	"bytes"
	"fmt"
	"log"
	"sync"
	"syscall"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
	"golang.org/x/sys/windows"
)

// --- Direct3D 11 / Windows Graphics Capture (WGC) via syscall ---

var (
	d3d11            = windows.NewLazySystemDLL("d3d11.dll")
	procCreateDevice = d3d11.NewProc("D3D11CreateDevice")
	procCreateDirect3D11DeviceFromDXGIDevice = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")

	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")

	user32               = windows.NewLazySystemDLL("user32.dll")
	procMonitorFromPoint = user32.NewProc("MonitorFromPoint")
)

// GUIDs
var (
	IID_IDXGIDevice     = windows.GUID{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	IID_ID3D11Texture2D = windows.GUID{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}

	// WGC interop GUIDs
	IID_IGraphicsCaptureItemInterop         = windows.GUID{0x3628e81b, 0x3cac, 0x4c60, [8]byte{0xb7, 0xf4, 0x23, 0xce, 0x0e, 0x0c, 0x33, 0x56}}
	IID_IDirect3DDxgiInterfaceAccess        = windows.GUID{0xa9b3d012, 0x3df2, 0x4ee3, [8]byte{0xb8, 0xd1, 0x86, 0x95, 0xf4, 0x57, 0xd3, 0xc1}}
	IID_IGraphicsCaptureItem                = windows.GUID{0x79c3f95b, 0x31f7, 0x4ec2, [8]byte{0xa4, 0x64, 0x63, 0x2e, 0xf5, 0xd3, 0x07, 0x60}}
	IID_IDirect3D11CaptureFramePoolStatics2 = windows.GUID{0x589b103f, 0x6bbc, 0x5df5, [8]byte{0xa9, 0x91, 0x02, 0xe2, 0x8b, 0x3b, 0x66, 0xd5}}
	IID_IGraphicsCaptureSession3            = windows.GUID{0x7e2204a4, 0x1f47, 0x5aaf, [8]byte{0x97, 0x54, 0x1c, 0x21, 0xb5, 0xb1, 0xc4, 0x52}}
)

const (
	DXGI_FORMAT_B8G8R8A8_UNORM = 87
	D3D11_SDK_VERSION          = 7
	D3D_DRIVER_TYPE_HARDWARE   = 1
	D3D11_CREATE_DEVICE_BGRA   = 0x20
	D3D11_USAGE_STAGING        = 3
	D3D11_CPU_ACCESS_READ      = 0x20000
	D3D11_MAP_READ             = 1

	RO_INIT_MULTITHREADED    = 1
	MONITOR_DEFAULTTOPRIMARY = 1
)

// d3d11Texture2DDesc matches the C D3D11_TEXTURE2D_DESC layout (44 bytes).
type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32 // DXGI_SAMPLE_DESC.Count
	SampleQuality  uint32 // DXGI_SAMPLE_DESC.Quality
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

// d3d11MappedSubresource matches the C D3D11_MAPPED_SUBRESOURCE layout.
type d3d11MappedSubresource struct {
	PData      uintptr
	RowPitch   uint32
	DepthPitch uint32
}

// --- WinRT helpers ---

func roInitialize() error {
	hr, _, _ := procRoInitialize.Call(RO_INIT_MULTITHREADED)
	if hr != 0 && hr != 1 { // S_OK or S_FALSE (already initialized)
		return fmt.Errorf("RoInitialize: 0x%X", hr)
	}
	return nil
}

func hstringCreate(s string) (uintptr, error) {
	u16, err := syscall.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var hs uintptr
	hr, _, _ := procWindowsCreateString.Call(
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)-1), // length excludes null terminator
		uintptr(unsafe.Pointer(&hs)),
	)
	if hr != 0 {
		return 0, fmt.Errorf("WindowsCreateString(%s): 0x%X", s, hr)
	}
	return hs, nil
}

func hstringDelete(hs uintptr) {
	if hs != 0 {
		procWindowsDeleteString.Call(hs)
	}
}

func roGetActivationFactory(className string, iid *windows.GUID) (uintptr, error) {
	hs, err := hstringCreate(className)
	if err != nil {
		return 0, err
	}
	defer hstringDelete(hs)

	var factory uintptr
	hr, _, _ := procRoGetActivationFactory.Call(
		hs,
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hr != 0 {
		return 0, fmt.Errorf("RoGetActivationFactory(%s): 0x%X", className, hr)
	}
	return factory, nil
}

// packSizeInt32 packs a WinRT SizeInt32{Width, Height} into a single register value.
func packSizeInt32(w, h int32) uintptr {
	return uintptr(uint32(w)) | (uintptr(uint32(h)) << 32)
}

// --- WGCCapturer ---

// WGCCapturer captures the screen using Windows Graphics Capture API.
type WGCCapturer struct {
	cfg           AgentConfig
	device        uintptr // ID3D11Device
	ctx           uintptr // ID3D11DeviceContext
	staging       uintptr // ID3D11Texture2D (staging, CPU-readable)
	winrtDevice   uintptr // IDirect3DDevice (WinRT wrapper)
	captureItem   uintptr // IGraphicsCaptureItem
	framePool     uintptr // IDirect3D11CaptureFramePool
	session       uintptr // IGraphicsCaptureSession
	width         int
	height        int
	stagingWidth  int
	stagingHeight int
	stagingFormat uint32
	tileSize      int
	prevFrame     []byte // previous full frame for tile-level delta detection
	encoder       *zstd.Encoder
	mu            sync.Mutex
}

func NewCapturer(cfg AgentConfig) (Capturer, error) {
	c := &WGCCapturer{
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

func (c *WGCCapturer) init() error {
	// 1. Initialize WinRT runtime
	if err := roInitialize(); err != nil {
		return err
	}

	// 2. Create D3D11 device
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

	// 3. Wrap D3D11 device as WinRT IDirect3DDevice
	var dxgiDevice uintptr
	hr = comQueryInterface(device, &IID_IDXGIDevice, &dxgiDevice)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IDXGIDevice: 0x%X", hr)
	}
	defer comRelease(dxgiDevice)

	var winrtDevice uintptr
	hr, _, _ = procCreateDirect3D11DeviceFromDXGIDevice.Call(
		dxgiDevice,
		uintptr(unsafe.Pointer(&winrtDevice)),
	)
	if hr != 0 {
		return fmt.Errorf("CreateDirect3D11DeviceFromDXGIDevice: 0x%X", hr)
	}
	c.winrtDevice = winrtDevice

	// 4. Get primary monitor HMONITOR
	hmonitor, _, _ := procMonitorFromPoint.Call(0, MONITOR_DEFAULTTOPRIMARY) // POINT{0,0}
	if hmonitor == 0 {
		return fmt.Errorf("MonitorFromPoint returned NULL")
	}

	// 5. Create IGraphicsCaptureItem for the monitor
	itemInterop, err := roGetActivationFactory(
		"Windows.Graphics.Capture.GraphicsCaptureItem",
		&IID_IGraphicsCaptureItemInterop,
	)
	if err != nil {
		return err
	}
	defer comRelease(itemInterop)

	var captureItem uintptr
	hr = comCall(itemInterop, 4, // IGraphicsCaptureItemInterop::CreateForMonitor
		hmonitor,
		uintptr(unsafe.Pointer(&IID_IGraphicsCaptureItem)),
		uintptr(unsafe.Pointer(&captureItem)),
	)
	if hr != 0 {
		return fmt.Errorf("CreateForMonitor: 0x%X", hr)
	}
	c.captureItem = captureItem

	// 6. Get capture item size (native monitor resolution)
	var size [2]int32 // SizeInt32{Width, Height}
	hr = comCall(c.captureItem, 7, uintptr(unsafe.Pointer(&size[0]))) // IGraphicsCaptureItem::get_Size
	if hr != 0 {
		return fmt.Errorf("get_Size: 0x%X", hr)
	}
	c.width = int(size[0])
	c.height = int(size[1])

	// 7. Create free-threaded frame pool
	fpFactory, err := roGetActivationFactory(
		"Windows.Graphics.Capture.Direct3D11CaptureFramePool",
		&IID_IDirect3D11CaptureFramePoolStatics2,
	)
	if err != nil {
		return err
	}
	defer comRelease(fpFactory)

	var framePool uintptr
	hr = comCall(fpFactory, 6, // IDirect3D11CaptureFramePoolStatics2::CreateFreeThreaded
		c.winrtDevice,
		uintptr(DXGI_FORMAT_B8G8R8A8_UNORM),
		2, // buffer count
		packSizeInt32(int32(c.width), int32(c.height)),
		uintptr(unsafe.Pointer(&framePool)),
	)
	if hr != 0 {
		return fmt.Errorf("CreateFreeThreaded: 0x%X", hr)
	}
	c.framePool = framePool

	// 8. Create capture session and start capturing
	var session uintptr
	hr = comCall(c.framePool, 10, // IDirect3D11CaptureFramePool::CreateCaptureSession
		c.captureItem,
		uintptr(unsafe.Pointer(&session)),
	)
	if hr != 0 {
		return fmt.Errorf("CreateCaptureSession: 0x%X", hr)
	}
	c.session = session

	hr = comCall(c.session, 6) // IGraphicsCaptureSession::StartCapture
	if hr != 0 {
		return fmt.Errorf("StartCapture: 0x%X", hr)
	}

	// 9. (Optional, Win11+) Disable yellow capture border
	var session3 uintptr
	hr = comQueryInterface(c.session, &IID_IGraphicsCaptureSession3, &session3)
	if hr == 0 {
		comCall(session3, 7, 0) // IGraphicsCaptureSession3::put_IsBorderRequired(false)
		comRelease(session3)
		log.Println("[capturer] disabled capture border")
	}

	log.Printf("[capturer] WGC initialized %dx%d tile=%d", c.width, c.height, c.tileSize)
	return nil
}

func (c *WGCCapturer) CaptureFrame() ([]proto.Tile, int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.framePool == 0 {
		return nil, 0, 0, fmt.Errorf("capture not initialized")
	}

	// 1. Try to get next captured frame
	var frame uintptr
	hr := comCall(c.framePool, 7, uintptr(unsafe.Pointer(&frame))) // TryGetNextFrame
	if hr != 0 {
		return nil, 0, 0, fmt.Errorf("TryGetNextFrame: 0x%X", hr)
	}
	if frame == 0 {
		return nil, c.width, c.height, nil // No new frame available
	}
	defer comRelease(frame)

	// 2. Get the frame's IDirect3DSurface
	var surface uintptr
	hr = comCall(frame, 6, uintptr(unsafe.Pointer(&surface))) // IDirect3D11CaptureFrame::get_Surface
	if hr != 0 {
		log.Printf("[capturer] get_Surface: 0x%X", hr)
		return nil, c.width, c.height, nil
	}
	defer comRelease(surface)

	// 3. Surface → IDirect3DDxgiInterfaceAccess → ID3D11Texture2D
	var access uintptr
	hr = comQueryInterface(surface, &IID_IDirect3DDxgiInterfaceAccess, &access)
	if hr != 0 {
		log.Printf("[capturer] QI IDirect3DDxgiInterfaceAccess: 0x%X", hr)
		return nil, c.width, c.height, nil
	}
	defer comRelease(access)

	var tex uintptr
	hr = comCall(access, 3, // IDirect3DDxgiInterfaceAccess::GetInterface
		uintptr(unsafe.Pointer(&IID_ID3D11Texture2D)),
		uintptr(unsafe.Pointer(&tex)),
	)
	if hr != 0 {
		log.Printf("[capturer] GetInterface ID3D11Texture2D: 0x%X", hr)
		return nil, c.width, c.height, nil
	}
	defer comRelease(tex)

	// 4. Read source texture dimensions
	var srcDesc d3d11Texture2DDesc
	comCall(tex, 10, uintptr(unsafe.Pointer(&srcDesc))) // ID3D11Texture2D::GetDesc
	if srcDesc.Width == 0 || srcDesc.Height == 0 {
		return nil, c.width, c.height, nil
	}

	// 5. Handle resolution change: recreate frame pool
	if c.width != int(srcDesc.Width) || c.height != int(srcDesc.Height) {
		c.width = int(srcDesc.Width)
		c.height = int(srcDesc.Height)
		c.prevFrame = nil // reset delta baseline
		comCall(c.framePool, 6, // IDirect3D11CaptureFramePool::Recreate
			c.winrtDevice,
			uintptr(DXGI_FORMAT_B8G8R8A8_UNORM),
			2,
			packSizeInt32(int32(c.width), int32(c.height)),
		)
		log.Printf("[capturer] resolution changed to %dx%d", c.width, c.height)
	}

	if !c.ensureStagingTexture(srcDesc) {
		return nil, c.width, c.height, nil
	}

	// 6. CopyResource: staging ← captured texture
	comCall(c.ctx, 47, c.staging, tex) // ID3D11DeviceContext::CopyResource

	// 7. Map staging texture for CPU read
	var mapped d3d11MappedSubresource
	hr = comCall(c.ctx, 14, // ID3D11DeviceContext::Map
		c.staging,
		0,              // Subresource
		D3D11_MAP_READ, // MapType
		0,              // MapFlags
		uintptr(unsafe.Pointer(&mapped)),
	)
	if hr != 0 {
		log.Printf("[capturer] Map staging: 0x%X", hr)
		return nil, c.width, c.height, nil
	}

	// 8. Extract changed tiles via delta comparison
	tiles := c.extractTilesDelta(mapped)

	// 9. Unmap
	comCall(c.ctx, 15, c.staging, 0) // ID3D11DeviceContext::Unmap

	return tiles, c.width, c.height, nil
}

// extractTilesDelta reads the mapped staging texture, compares each tile against
// prevFrame, and returns only the tiles that changed (zstd-compressed BGRA).
func (c *WGCCapturer) extractTilesDelta(mapped d3d11MappedSubresource) []proto.Tile {
	rowPitch := int(mapped.RowPitch)
	ts := c.tileSize
	frameSize := c.width * c.height * 4

	// Copy frame into a contiguous buffer (staging may have padding per row)
	cur := make([]byte, frameSize)
	for y := 0; y < c.height; y++ {
		src := mapped.PData + uintptr(y*rowPitch)
		copy(cur[y*c.width*4:(y+1)*c.width*4],
			unsafe.Slice((*byte)(unsafe.Pointer(src)), c.width*4))
	}

	var tiles []proto.Tile
	tilesX := (c.width + ts - 1) / ts
	tilesY := (c.height + ts - 1) / ts

	for ty := 0; ty < tilesY; ty++ {
		for tx := 0; tx < tilesX; tx++ {
			tileW := ts
			tileH := ts
			if (tx+1)*ts > c.width {
				tileW = c.width - tx*ts
			}
			if (ty+1)*ts > c.height {
				tileH = c.height - ty*ts
			}
			if tileW <= 0 || tileH <= 0 {
				continue
			}

			// Skip unchanged tiles (bytes.Equal short-circuits on first diff)
			if c.prevFrame != nil && c.tileUnchanged(cur, tx, ty, tileW, tileH) {
				continue
			}

			raw := make([]byte, tileW*tileH*4)
			for row := 0; row < tileH; row++ {
				srcOff := (ty*ts+row)*c.width*4 + tx*ts*4
				dstOff := row * tileW * 4
				copy(raw[dstOff:dstOff+tileW*4], cur[srcOff:srcOff+tileW*4])
			}

			compressed := c.encoder.EncodeAll(raw, nil)
			tiles = append(tiles, proto.Tile{
				TX:      uint16(tx),
				TY:      uint16(ty),
				Codec:   proto.CodecZstdRawBGRA,
				DataLen: uint32(len(compressed)),
				Data:    compressed,
			})
		}
	}

	c.prevFrame = cur
	return tiles
}

// tileUnchanged returns true if the tile at (tx,ty) is identical in cur and prevFrame.
func (c *WGCCapturer) tileUnchanged(cur []byte, tx, ty, tileW, tileH int) bool {
	ts := c.tileSize
	for row := 0; row < tileH; row++ {
		off := (ty*ts+row)*c.width*4 + tx*ts*4
		if !bytes.Equal(cur[off:off+tileW*4], c.prevFrame[off:off+tileW*4]) {
			return false
		}
	}
	return true
}

func (c *WGCCapturer) ensureStagingTexture(src d3d11Texture2DDesc) bool {
	w := int(src.Width)
	h := int(src.Height)
	needNew := c.staging == 0 || c.stagingWidth != w || c.stagingHeight != h || c.stagingFormat != src.Format
	if !needNew {
		return true
	}
	if c.staging != 0 {
		comRelease(c.staging)
		c.staging = 0
	}

	desc := d3d11Texture2DDesc{
		Width:          src.Width,
		Height:         src.Height,
		MipLevels:      1,
		ArraySize:      1,
		Format:         src.Format,
		SampleCount:    1,
		SampleQuality:  0,
		Usage:          D3D11_USAGE_STAGING,
		BindFlags:      0,
		CPUAccessFlags: D3D11_CPU_ACCESS_READ,
		MiscFlags:      0,
	}

	var staging uintptr
	hr := comCall(c.device, 5, // ID3D11Device::CreateTexture2D
		uintptr(unsafe.Pointer(&desc)),
		0, // pInitialData (NULL)
		uintptr(unsafe.Pointer(&staging)),
	)
	if hr != 0 {
		log.Printf("[capturer] CreateTexture2D staging: 0x%X", hr)
		return false
	}

	c.staging = staging
	c.stagingWidth = w
	c.stagingHeight = h
	c.stagingFormat = src.Format
	return true
}

func (c *WGCCapturer) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Release in reverse order of creation
	if c.session != 0 {
		comRelease(c.session)
		c.session = 0
	}
	if c.framePool != 0 {
		comRelease(c.framePool)
		c.framePool = 0
	}
	if c.captureItem != 0 {
		comRelease(c.captureItem)
		c.captureItem = 0
	}
	if c.winrtDevice != 0 {
		comRelease(c.winrtDevice)
		c.winrtDevice = 0
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
	r, _, _ := syscall.SyscallN(vtable[0], obj, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out)))
	return uintptr(r)
}

func comRelease(obj uintptr) {
	if obj == 0 {
		return
	}
	vtable := *(*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	syscall.SyscallN(vtable[2], obj)
}

func comCall(obj uintptr, method int, args ...uintptr) uintptr {
	vtable := *(*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	allArgs := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(vtable[method], allArgs...)
	return uintptr(r)
}
