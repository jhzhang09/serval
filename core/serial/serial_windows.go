//go:build windows

package serial

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32DLL         = windows.NewLazySystemDLL("kernel32.dll")
	procGetCommState    = kernel32DLL.NewProc("GetCommState")
	procSetCommState    = kernel32DLL.NewProc("SetCommState")
	procSetCommTimeouts = kernel32DLL.NewProc("SetCommTimeouts")
	procPurgeComm       = kernel32DLL.NewProc("PurgeComm")
)

const (
	winBinaryFlag = 0x0001
	winParityFlag = 0x0002

	winNoParity    = 0
	winOddParity   = 1
	winEvenParity  = 2
	winMarkParity  = 3
	winSpaceParity = 4

	winOneStopBit   = 0
	winOne5StopBits = 1
	winTwoStopBits  = 2

	purgeRxAbort = 0x0002
	purgeTxAbort = 0x0001
	purgeRxClear = 0x0008
	purgeTxClear = 0x0004
)

// winDCB 对应 Win32 DCB 结构体（28 字节）。
type winDCB struct {
	DCBlength  uint32
	BaudRate   uint32
	Flags      uint32
	WReserved  uint16
	XonLim     uint16
	XoffLim    uint16
	ByteSize   byte
	Parity     byte
	StopBits   byte
	XonChar    byte
	XoffChar   byte
	ErrorChar  byte
	EofChar    byte
	EvtChar    byte
	WReserved1 uint16
}

// winCommTimeouts 对应 Win32 COMMTIMEOUTS 结构体（20 字节）。
type winCommTimeouts struct {
	ReadIntervalTimeout         uint32
	ReadTotalTimeoutMultiplier  uint32
	ReadTotalTimeoutConstant    uint32
	WriteTotalTimeoutMultiplier uint32
	WriteTotalTimeoutConstant   uint32
}

// normalizeWinPortName 将 COM3 等短名统一为 \\.\COM3（COM10 及以上必须带此前缀）。
func normalizeWinPortName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, `\\.\`) || strings.HasPrefix(trimmed, `\\?\`) {
		return trimmed
	}
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "COM") {
		return `\\.\` + trimmed
	}
	return trimmed
}

func applyWinDCB(dcb *winDCB, cfg Config) error {
	if cfg.BaudRate <= 0 {
		return ErrUnsupportedBaudRate
	}
	if cfg.DataBits < 5 || cfg.DataBits > 8 {
		return ErrInvalidConfig
	}
	var stopBits byte
	switch cfg.StopBits {
	case StopBits1:
		stopBits = winOneStopBit
	case StopBits1_5:
		stopBits = winOne5StopBits
	case StopBits2:
		stopBits = winTwoStopBits
	default:
		return ErrInvalidConfig
	}
	var parity byte
	switch cfg.Parity {
	case ParityNone, "":
		parity = winNoParity
	case ParityOdd:
		parity = winOddParity
	case ParityEven:
		parity = winEvenParity
	case ParityMark:
		parity = winMarkParity
	case ParitySpace:
		parity = winSpaceParity
	default:
		return ErrInvalidConfig
	}

	dcb.BaudRate = uint32(cfg.BaudRate)
	dcb.ByteSize = byte(cfg.DataBits)
	dcb.Parity = parity
	dcb.StopBits = stopBits
	// 清空流控及奇偶校验开关，仅启用 fBinary 和按需开启 fParity。
	dcb.Flags &^= 0x7FFF
	dcb.Flags |= winBinaryFlag
	if parity != winNoParity {
		dcb.Flags |= winParityFlag
	}
	return nil
}

// winSerialPort 通过 Win32 Overlapped I/O 实现全双工读写，避免同步句柄上 Read 阻塞 Write，
// 并在 Close 时立刻唤醒阻塞中的 Read。
type winSerialPort struct {
	handle    windows.Handle
	readMu    sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
	closed    bool
	closeMu   sync.Mutex
	readEv    windows.Handle
	writeEv   windows.Handle
	closeEv   windows.Handle
}

func (p *winSerialPort) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	p.readMu.Lock()
	defer p.readMu.Unlock()

	for {
		p.closeMu.Lock()
		closed := p.closed
		p.closeMu.Unlock()
		if closed {
			return 0, io.EOF
		}

		if err := windows.ResetEvent(p.readEv); err != nil {
			return 0, err
		}
		var ov windows.Overlapped
		ov.HEvent = p.readEv

		var n uint32
		err := windows.ReadFile(p.handle, b, &n, &ov)
		if err == nil {
			if n > 0 {
				return int(n), nil
			}
			continue
		}
		if !errors.Is(err, windows.ERROR_IO_PENDING) {
			if errors.Is(err, windows.ERROR_OPERATION_ABORTED) || errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				return 0, io.EOF
			}
			return 0, err
		}

		handles := []windows.Handle{p.readEv, p.closeEv}
		waitRes, waitErr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if waitErr != nil {
			return 0, waitErr
		}
		if waitRes == windows.WAIT_OBJECT_0+1 {
			_ = windows.CancelIoEx(p.handle, &ov)
			return 0, io.EOF
		}

		if err := windows.GetOverlappedResult(p.handle, &ov, &n, true); err != nil {
			if errors.Is(err, windows.ERROR_OPERATION_ABORTED) || errors.Is(err, windows.ERROR_INVALID_HANDLE) {
				return 0, io.EOF
			}
			return 0, err
		}
		if n > 0 {
			return int(n), nil
		}
	}
}

func (p *winSerialPort) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	p.closeMu.Lock()
	closed := p.closed
	p.closeMu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}

	if err := windows.ResetEvent(p.writeEv); err != nil {
		return 0, err
	}
	var ov windows.Overlapped
	ov.HEvent = p.writeEv

	var n uint32
	err := windows.WriteFile(p.handle, b, &n, &ov)
	if err == nil {
		return int(n), nil
	}
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		return int(n), err
	}

	handles := []windows.Handle{p.writeEv, p.closeEv}
	waitRes, waitErr := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
	if waitErr != nil {
		return 0, waitErr
	}
	if waitRes == windows.WAIT_OBJECT_0+1 {
		_ = windows.CancelIoEx(p.handle, &ov)
		return 0, io.ErrClosedPipe
	}
	if err := windows.GetOverlappedResult(p.handle, &ov, &n, true); err != nil {
		return int(n), err
	}
	return int(n), nil
}

func (p *winSerialPort) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.closeMu.Lock()
		p.closed = true
		p.closeMu.Unlock()

		if p.closeEv != 0 {
			_ = windows.SetEvent(p.closeEv)
		}
		_, _, _ = procPurgeComm.Call(uintptr(p.handle), purgeRxAbort|purgeTxAbort|purgeRxClear|purgeTxClear)
		closeErr = windows.CloseHandle(p.handle)
		if p.readEv != 0 {
			_ = windows.CloseHandle(p.readEv)
		}
		if p.writeEv != 0 {
			_ = windows.CloseHandle(p.writeEv)
		}
		if p.closeEv != 0 {
			_ = windows.CloseHandle(p.closeEv)
		}
	})
	return closeErr
}

// Open 在 Windows 下通过 Win32 API 打开并配置串口。
func Open(cfg Config) (*Port, error) {
	if cfg.BaudRate <= 0 {
		cfg.BaudRate = 115200
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = StopBits1
	}
	if cfg.Parity == "" {
		cfg.Parity = ParityNone
	}

	devName := normalizeWinPortName(cfg.Name)
	if devName == "" {
		return nil, fmt.Errorf("%w: 空设备名", ErrInvalidConfig)
	}
	namePtr, err := windows.UTF16PtrFromString(devName)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	handle, err := windows.CreateFile(
		namePtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
		0,
	)
	if err != nil {
		switch {
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND), errors.Is(err, windows.ERROR_PATH_NOT_FOUND):
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cfg.Name)
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return nil, fmt.Errorf("%w: %s", ErrPermission, cfg.Name)
		case errors.Is(err, windows.ERROR_SHARING_VIOLATION):
			return nil, fmt.Errorf("%w: %s", ErrBusy, cfg.Name)
		}
		return nil, fmt.Errorf("打开串口 %s 失败: %w", cfg.Name, err)
	}

	cleanup := func() {
		_ = windows.CloseHandle(handle)
	}

	var dcb winDCB
	dcb.DCBlength = uint32(unsafe.Sizeof(dcb))
	r1, _, e1 := procGetCommState.Call(uintptr(handle), uintptr(unsafe.Pointer(&dcb)))
	if r1 == 0 {
		cleanup()
		return nil, fmt.Errorf("读取串口 %s 状态失败: %w", cfg.Name, e1)
	}
	if err := applyWinDCB(&dcb, cfg); err != nil {
		cleanup()
		return nil, err
	}
	r1, _, e1 = procSetCommState.Call(uintptr(handle), uintptr(unsafe.Pointer(&dcb)))
	if r1 == 0 {
		cleanup()
		if errors.Is(e1, syscall.Errno(87)) { // ERROR_INVALID_PARAMETER
			return nil, fmt.Errorf("%w: %d", ErrUnsupportedBaudRate, cfg.BaudRate)
		}
		return nil, fmt.Errorf("设置串口 %s 参数失败: %w", cfg.Name, e1)
	}

	// 读取至少等待第一字节或短间隔返回，配合 Overlapped 循环既不忙等也不卡死关闭。
	timeouts := winCommTimeouts{
		ReadIntervalTimeout:         50,
		ReadTotalTimeoutMultiplier:  0,
		ReadTotalTimeoutConstant:    250,
		WriteTotalTimeoutMultiplier: 0,
		WriteTotalTimeoutConstant:   5000,
	}
	r1, _, e1 = procSetCommTimeouts.Call(uintptr(handle), uintptr(unsafe.Pointer(&timeouts)))
	if r1 == 0 {
		cleanup()
		return nil, fmt.Errorf("设置串口 %s 超时失败: %w", cfg.Name, e1)
	}

	readEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		cleanup()
		return nil, err
	}
	writeEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(readEv)
		cleanup()
		return nil, err
	}
	closeEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(readEv)
		_ = windows.CloseHandle(writeEv)
		cleanup()
		return nil, err
	}

	return &Port{rwc: &winSerialPort{
		handle:  handle,
		readEv:  readEv,
		writeEv: writeEv,
		closeEv: closeEv,
	}}, nil
}

// sortCOMPorts 将 COM1, COM2, ..., COM10 按数字顺序自然排序。
func sortCOMPorts(ports []string) {
	comNum := func(s string) (int, bool) {
		up := strings.ToUpper(strings.TrimSpace(s))
		if !strings.HasPrefix(up, "COM") {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimPrefix(up, "COM"))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	sort.Slice(ports, func(i, j int) bool {
		ni, oki := comNum(ports[i])
		nj, okj := comNum(ports[j])
		if oki && okj && ni != nj {
			return ni < nj
		}
		if oki != okj {
			return oki
		}
		return ports[i] < ports[j]
	})
}

// ListPorts 通过 Windows 注册表 HARDWARE\DEVICEMAP\SERIALCOMM 枚举可用串口。
func ListPorts() []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DEVICEMAP\SERIALCOMM`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()

	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		val, _, err := k.GetStringValue(name)
		if err != nil {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" || seen[val] {
			continue
		}
		seen[val] = true
		out = append(out, val)
	}
	sortCOMPorts(out)
	return out
}
