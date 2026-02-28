//go:build windows

package main

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"unsafe"

	"github.com/klauspost/compress/zstd"
	"github.com/opsview/opsview/proto"
	"golang.org/x/sys/windows"
)

// --- Direct3D 11 / DXGI COM interfaces via syscall ---

var (
	d3d11            = windows.NewLazySystemDLL("d3d11.dll")
	procCreateDevice = d3d11.NewProc("D3D11CreateDevice")
	dxgi             = windows.NewLazySystemDLL("dxgi.dll")
)

// GUIDs
var (
	IID_IDXGIDevice     = windows.GUID{0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	IID_IDXGIAdapter    = windows.GUID{0x2411e7e1, 0x12ac, 0x4ccf, [8]byte{0xbd, 0x14, 0x97, 0x98, 0xe8, 0x53, 0x4d, 0xc0}}
	IID_IDXGIOutput1    = windows.GUID{0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	IID_ID3D11Texture2D = windows.GUID{0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

const (
	DXGI_FORMAT_B8G8R8A8_UNORM = 87
	D3D11_SDK_VERSION          = 7
	D3D_DRIVER_TYPE_HARDWARE   = 1
	D3D11_CREATE_DEVICE_BGRA   = 0x20
	D3D11_USAGE_STAGING        = 3
	D3D11_CPU_ACCESS_READ      = 0x20000
	DXGI_MAP_READ              = 1
	DXGI_ERROR_WAIT_TIMEOUT    = 0x887A0027
	DXGI_ERROR_ACCESS_LOST     = 0x887A0026
	DXGI_ERROR_ACCESS_DENIED   = 0x887A002B
	D3D11_MAP_READ             = 1
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

// DXGICapturer captures the screen using DXGI Desktop Duplication.
type DXGICapturer struct {
	cfg           AgentConfig
	device        uintptr // *ID3D11Device
	ctx           uintptr // *ID3D11DeviceContext
	dup           uintptr // *IDXGIOutputDuplication
	staging       uintptr // *ID3D11Texture2D
	stagingWidth  int
	stagingHeight int
	stagingFormat uint32
	width         int
	height        int
	tileSize      int
	encoder       *zstd.Encoder
	prevFrame     []byte // previous full frame for delta detection
	firstFrame    bool
	mu            sync.Mutex
}

func NewCapturer(cfg AgentConfig) (Capturer, error) {
	c := &DXGICapturer{
		cfg:        cfg,
		tileSize:   cfg.TileSize,
		firstFrame: true,
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

// ID3D11DeviceContext vtable indices (from d3d11.h ID3D11DeviceContextVtbl):
//
//	IUnknown:        0=QI, 1=AddRef, 2=Release
//	ID3D11DeviceChild: 3=GetDevice, 4=GetPrivateData, 5=SetPrivateData, 6=SetPrivateDataInterface
//	ID3D11DeviceContext (starting at 7):
//	  7=VSSetConstantBuffers, 8=PSSetShaderResources, 9=PSSetShader,
//	  10=PSSetSamplers, 11=VSSetShader, 12=DrawIndexed, 13=Draw,
//	  14=Map, 15=Unmap, 16=PSSetConstantBuffers, ...
//	  46=CopySubresourceRegion, 47=CopyResource, 48=UpdateSubresource, ...
//	  105=Flush, 106=GetType
//
// IDXGIOutputDuplication vtable indices (IDL declaration order):
//
//	IUnknown: 0=QI, 1=AddRef, 2=Release
//	IDXGIObject: 3=SetPrivateData, 4=SetPrivateDataInterface, 5=GetPrivateData, 6=GetParent
//	IDXGIOutputDuplication:
//	  7=GetDesc, 8=AcquireNextFrame, 9=GetFrameDirtyRects,
//	  10=GetFrameMoveRects, 11=GetFramePointerShape,
//	  12=MapDesktopSurface, 13=UnMapDesktopSurface, 14=ReleaseFrame
const (
	// ID3D11DeviceContext
	vtCtxMap          = 14
	vtCtxUnmap        = 15
	vtCtxCopyResource = 47
	vtCtxFlush        = 105

	// ID3D11Device
	vtDevCreateTexture2D        = 5
	vtDevGetDeviceRemovedReason = 39

	// IDXGIOutputDuplication
	vtDupGetDesc            = 7
	vtDupAcquireNextFrame   = 8
	vtDupGetFrameDirtyRects = 9
	vtDupReleaseFrame       = 14

	// ID3D11Texture2D (inherits ID3D11Resource inherits ID3D11DeviceChild)
	vtTexGetDesc = 10
)

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

	// Get IDXGIDevice -> IDXGIAdapter -> IDXGIOutput -> IDXGIOutput1
	var dxgiDevice uintptr
	hr = comQueryInterface(device, &IID_IDXGIDevice, &dxgiDevice)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IDXGIDevice: 0x%X", hr)
	}
	defer comRelease(dxgiDevice)

	// IDXGIDevice::GetAdapter (vtable[7])
	// CRITICAL: Use direct SyscallN for calls that pass Go-stack pointers.
	// Passing unsafe.Pointer -> uintptr through an intermediate Go function (comCall)
	// is unsafe because goroutine stack growth can move the stack between the
	// uintptr conversion and the actual syscall, leaving stale pointers.
	var adapter uintptr
	r, _, _ := syscall.SyscallN(comVtbl(dxgiDevice)[7],
		dxgiDevice, uintptr(unsafe.Pointer(&adapter)))
	if uintptr(r) != 0 {
		return fmt.Errorf("GetAdapter: 0x%X", r)
	}
	defer comRelease(adapter)

	// IDXGIAdapter::EnumOutputs(0)
	var output uintptr
	r, _, _ = syscall.SyscallN(comVtbl(adapter)[7],
		adapter, 0, uintptr(unsafe.Pointer(&output)))
	if uintptr(r) != 0 {
		return fmt.Errorf("EnumOutputs: 0x%X", r)
	}
	defer comRelease(output)

	var output1 uintptr
	hr = comQueryInterface(output, &IID_IDXGIOutput1, &output1)
	if hr != 0 {
		return fmt.Errorf("QueryInterface IDXGIOutput1: 0x%X", hr)
	}
	defer comRelease(output1)

	// IDXGIOutput1::DuplicateOutput
	var dup uintptr
	r, _, _ = syscall.SyscallN(comVtbl(output1)[22],
		output1, uintptr(device), uintptr(unsafe.Pointer(&dup)))
	if uintptr(r) != 0 {
		return fmt.Errorf("DuplicateOutput: 0x%X", r)
	}
	c.dup = dup

	// Get actual desktop resolution from GetDesc (vtable[7] on dup)
	var dupDesc [48]byte // DXGI_OUTDUPL_DESC (36 bytes, pad to 48)
	syscall.SyscallN(comVtbl(dup)[vtDupGetDesc],
		dup, uintptr(unsafe.Pointer(&dupDesc[0])))
	c.width = int(*(*uint32)(unsafe.Pointer(&dupDesc[0])))
	c.height = int(*(*uint32)(unsafe.Pointer(&dupDesc[4])))
	if c.width == 0 || c.height == 0 {
		c.width = 1920
		c.height = 1080
	}

	log.Printf("[capturer] initialized DXGI %dx%d tile=%d device=%#x ctx=%#x dup=%#x",
		c.width, c.height, c.tileSize, c.device, c.ctx, c.dup)
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
	dupVt := comVtbl(c.dup)
	r, _, _ := syscall.SyscallN(dupVt[vtDupAcquireNextFrame],
		c.dup,
		100, // TimeoutInMilliseconds
		uintptr(unsafe.Pointer(&frameInfo[0])),
		uintptr(unsafe.Pointer(&resource)),
	)
	hr := uintptr(r)
	if hr == DXGI_ERROR_WAIT_TIMEOUT {
		return nil, c.width, c.height, nil // No changes
	}
	if hr == DXGI_ERROR_ACCESS_LOST || hr == DXGI_ERROR_ACCESS_DENIED {
		return nil, 0, 0, fmt.Errorf("access lost (0x%X), need reinit", hr)
	}
	if hr != 0 {
		return nil, 0, 0, fmt.Errorf("AcquireNextFrame: 0x%X", hr)
	}
	defer comCall(c.dup, vtDupReleaseFrame)
	defer comRelease(resource)

	// Map the frame texture and extract changed tiles.
	tiles := c.extractTiles(resource)

	return tiles, c.width, c.height, nil
}

type rect struct {
	left, top, right, bottom int
}

func (c *DXGICapturer) getDirtyRects() []rect {
	buf := make([]byte, 4096)
	var needed uint32

	if c.firstFrame {
		c.firstFrame = false
		return []rect{{0, 0, c.width, c.height}}
	}

	dupVt := comVtbl(c.dup)
	r, _, _ := syscall.SyscallN(dupVt[vtDupGetFrameDirtyRects],
		c.dup,
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&needed)),
	)
	if uintptr(r) != 0 || needed == 0 {
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

func (c *DXGICapturer) extractTiles(resource uintptr) []proto.Tile {
	// 1. QueryInterface resource -> ID3D11Texture2D
	var tex uintptr
	hr := comQueryInterface(resource, &IID_ID3D11Texture2D, &tex)
	if hr != 0 {
		log.Printf("[capturer] QI ID3D11Texture2D: 0x%X", hr)
		return nil
	}
	defer comRelease(tex)

	// 2. Read source texture dimensions (direct SyscallN for Go-stack pointer).
	var srcDesc d3d11Texture2DDesc
	syscall.SyscallN(comVtbl(tex)[vtTexGetDesc],
		tex, uintptr(unsafe.Pointer(&srcDesc)))
	if srcDesc.Width == 0 || srcDesc.Height == 0 {
		log.Printf("[capturer] invalid source texture %dx%d", srcDesc.Width, srcDesc.Height)
		return nil
	}
	if c.width != int(srcDesc.Width) || c.height != int(srcDesc.Height) {
		c.width = int(srcDesc.Width)
		c.height = int(srcDesc.Height)
		log.Printf("[capturer] resolution changed %dx%d", c.width, c.height)
	}
	if !c.ensureStagingTexture(srcDesc) {
		return nil
	}

	// Build set of dirty tile coordinates.
	dirtyRects := c.getDirtyRects()
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
	if len(tileSet) == 0 {
		return nil
	}

	// 3. CopyResource: staging <- frame texture (no Go-stack pointers, comCall is safe)
	comCall(c.ctx, vtCtxCopyResource, c.staging, tex)

	// 4. Map staging texture for CPU read (direct SyscallN for &mapped on Go stack)
	var mapped d3d11MappedSubresource
	ctxVt := comVtbl(c.ctx)
	r, _, _ := syscall.SyscallN(ctxVt[vtCtxMap],
		c.ctx,
		c.staging,
		0,              // Subresource
		D3D11_MAP_READ, // MapType
		0,              // MapFlags
		uintptr(unsafe.Pointer(&mapped)),
	)
	if uintptr(r) != 0 {
		log.Printf("[capturer] Map failed: 0x%X staging=%#x", r, c.staging)
		return nil
	}

	// 5. Extract BGRA pixels for each dirty tile
	rowPitch := int(mapped.RowPitch)
	var tiles []proto.Tile
	for coord := range tileSet {
		tx, ty := coord[0], coord[1]
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

		raw := make([]byte, tileW*tileH*4)
		srcBase := mapped.PData + uintptr(ty*ts*rowPitch+tx*ts*4)
		for row := 0; row < tileH; row++ {
			src := srcBase + uintptr(row*rowPitch)
			dst := raw[row*tileW*4 : (row+1)*tileW*4]
			copy(dst, unsafe.Slice((*byte)(unsafe.Pointer(src)), tileW*4))
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

	// 6. Unmap staging texture (no Go-stack pointers, comCall is safe)
	comCall(c.ctx, vtCtxUnmap, c.staging, 0)

	return tiles
}

func (c *DXGICapturer) ensureStagingTexture(src d3d11Texture2DDesc) bool {
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

	// Direct SyscallN: &desc and &staging are Go-stack pointers.
	var staging uintptr
	r, _, _ := syscall.SyscallN(comVtbl(c.device)[vtDevCreateTexture2D],
		c.device,
		uintptr(unsafe.Pointer(&desc)),
		0, // pInitialData (NULL)
		uintptr(unsafe.Pointer(&staging)),
	)
	if uintptr(r) != 0 {
		log.Printf("[capturer] CreateTexture2D staging failed: 0x%X", r)
		return false
	}
	if staging == 0 {
		log.Printf("[capturer] CreateTexture2D returned nil staging")
		return false
	}

	c.staging = staging
	c.stagingWidth = w
	c.stagingHeight = h
	c.stagingFormat = src.Format
	log.Printf("[capturer] created staging %dx%d fmt=%d ptr=%#x", w, h, src.Format, staging)
	return true
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

// comVtbl reads the vtable pointer array from a COM object.
// This returns a pointer to native (non-Go) memory, so it is safe
// to use across potential goroutine stack moves.
func comVtbl(obj uintptr) *[1024]uintptr {
	return (*[1024]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
}

func comQueryInterface(obj uintptr, iid *windows.GUID, out *uintptr) uintptr {
	r, _, _ := syscall.SyscallN(comVtbl(obj)[0],
		obj, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out)))
	return uintptr(r)
}

func comRelease(obj uintptr) {
	if obj == 0 {
		return
	}
	syscall.SyscallN(comVtbl(obj)[2], obj)
}

// comCall calls a COM method where all args are non-Go-memory uintptrs
// (COM object pointers or integer values). Do NOT pass Go-stack pointers
// through this function; use direct syscall.SyscallN instead.
func comCall(obj uintptr, method int, args ...uintptr) uintptr {
	allArgs := append([]uintptr{obj}, args...)
	r, _, _ := syscall.SyscallN(comVtbl(obj)[method], allArgs...)
	return uintptr(r)
}
