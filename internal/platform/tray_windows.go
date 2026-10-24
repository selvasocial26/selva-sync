//go:build windows

package platform

import (
	"encoding/binary"
	"sync"
	"syscall"
	"unsafe"
)

var (
	procRegisterClassExW         = user32.NewProc("RegisterClassExW")
	procCreateWindowExW          = user32.NewProc("CreateWindowExW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procGetMessageW              = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessageW         = user32.NewProc("DispatchMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procLoadCursorW              = user32.NewProc("LoadCursorW")
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmQueryEnd     = 0x0011
	wmEndSession   = 0x0016
	wmDestroy      = 0x0002
	wmClose        = 0x0010
	wmCommand      = 0x0111
	wmLButtonUp    = 0x0202
	wmRButtonUp    = 0x0205
	wmApp          = 0x8000
	wmTray         = wmApp + 1
	ninBalloonUser = 0x0405

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	nifShowTip = 0x80
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type point struct{ X, Y int32 }

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type notifyIconData struct {
	Size             uint32
	Wnd              uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUIDItem         [16]byte
	BalloonIcon      uintptr
}

// Tray is the notification-area icon.
type Tray struct {
	cfg      TrayConfig
	hwnd     uintptr
	icon     uintptr
	mu       sync.Mutex
	tip      string
	reAddMsg uint32
}

var theTray *Tray

func copyUTF16(dst []uint16, s string) {
	u, _ := syscall.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	for i := range dst {
		dst[i] = 0
	}
	copy(dst, u)
}

// iconFromICO picks the best image from an .ico file for the given size.
func iconFromICO(ico []byte, size int) uintptr {
	if len(ico) < 6 {
		return 0
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	best, bestDiff := -1, 1<<30
	for i := 0; i < count; i++ {
		e := ico[6+16*i:]
		if len(e) < 16 {
			break
		}
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		d := w - size
		if d < 0 {
			d = -d*2 + 1 // prefer larger images (scaled down looks better)
		}
		if d < bestDiff {
			best, bestDiff = i, d
		}
	}
	if best < 0 {
		return 0
	}
	e := ico[6+16*best:]
	n := binary.LittleEndian.Uint32(e[8:])
	off := binary.LittleEndian.Uint32(e[12:])
	if int(off+n) > len(ico) {
		return 0
	}
	img := ico[off : off+n]
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&img[0])), uintptr(n), 1, 0x00030000, uintptr(size), uintptr(size), 0)
	return h
}

func (t *Tray) nid() *notifyIconData {
	d := &notifyIconData{Wnd: t.hwnd, ID: 1, CallbackMessage: wmTray, Icon: t.icon}
	d.Size = uint32(unsafe.Sizeof(*d))
	return d
}

func (t *Tray) add() {
	d := t.nid()
	d.Flags = nifMessage | nifIcon | nifTip | nifShowTip
	copyUTF16(d.Tip[:], t.tip)
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(d)))
}

// SetTooltip changes the hover text.
func (t *Tray) SetTooltip(s string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tip = s
	d := t.nid()
	d.Flags = nifTip | nifShowTip
	copyUTF16(d.Tip[:], s)
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
}

// Notify shows a Windows notification from the tray icon.
func (t *Tray) Notify(title, text string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.nid()
	d.Flags = nifInfo
	copyUTF16(d.InfoTitle[:], title)
	copyUTF16(d.Info[:], text)
	d.InfoFlags = 0x04 | 0x20 // NIIF_USER | NIIF_LARGE_ICON (use app icon)
	d.BalloonIcon = t.icon
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(d)))
}

// Quit removes the icon and ends RunTray.
func (t *Tray) Quit() {
	if t == nil {
		return
	}
	procPostMessageW.Call(t.hwnd, wmClose, 0, 0)
}

func (t *Tray) showMenu() {
	items := t.cfg.Menu()
	menu, _, _ := procCreatePopupMenu.Call()
	for _, it := range items {
		if it.ID == 0 {
			procAppendMenuW.Call(menu, 0x800, 0, 0) // MF_SEPARATOR
			continue
		}
		flags := uintptr(0)
		if it.ID < 0 {
			flags = 0x1 // MF_GRAYED (status line)
		}
		id := it.ID
		if id < 0 {
			id = -id
		}
		procAppendMenuW.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(u16(it.Label))))
	}
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(menu, 0x100|0x80|0x2|0x20, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0) // RETURNCMD|NONOTIFY|RIGHTBUTTON|BOTTOMALIGN
	procDestroyMenu.Call(menu)
	procPostMessageW.Call(t.hwnd, 0, 0, 0)
	if cmd != 0 && t.cfg.OnMenu != nil {
		go t.cfg.OnMenu(int(cmd))
	}
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	t := theTray
	switch uint32(msg) {
	case wmTray:
		switch uint32(lparam) & 0xffff {
		case wmLButtonUp, ninBalloonUser:
			if t.cfg.OnOpen != nil {
				go t.cfg.OnOpen()
			}
		case wmRButtonUp:
			t.showMenu()
		}
		return 0
	case wmQueryEnd:
		return 1 // allow Windows to shut down
	case wmEndSession:
		if wparam != 0 && t.cfg.OnEndSession != nil {
			t.cfg.OnEndSession() // save work before Windows ends the process
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		d := t.nid()
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(d)))
		procPostQuitMessage.Call(0)
		return 0
	}
	if t != nil && t.reAddMsg != 0 && uint32(msg) == t.reAddMsg {
		t.add() // Explorer restarted
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// RunTray shows the icon and runs the Windows message loop until Quit.
// Must be called from the main goroutine (locked to the OS thread).
func RunTray(cfg TrayConfig) *Tray {
	t := &Tray{cfg: cfg, tip: cfg.Tooltip}
	theTray = t
	inst := moduleHandle()
	cls := u16("SelvaSyncTrayWindow")
	cursor, _, _ := procLoadCursorW.Call(0, 32512)
	wc := wndClassEx{WndProc: syscall.NewCallback(wndProc), Instance: inst, ClassName: cls, Cursor: cursor}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	t.hwnd, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("Selva Sync"))), 0, 0, 0, 0, 0, 0, 0, inst, 0)
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))
	t.reAddMsg = uint32(r)
	sz, _, _ := procGetSystemMetrics.Call(49) // SM_CXSMICON
	if sz == 0 {
		sz = 16
	}
	t.icon = iconFromICO(cfg.Icon, int(sz))
	t.add()
	if cfg.OnReady != nil {
		go cfg.OnReady()
	}
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return t
}

// Current returns the running tray (nil before RunTray).
func Current() *Tray { return theTray }
