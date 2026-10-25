//go:build windows

package main

// Windows 划词翻译：RegisterHotKey 注册全局快捷键，触发后向当前程序
// 发送 Ctrl+C 复制选中文本，再从剪贴板取回内容交给主窗口翻译。

import (
	goruntime "runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	hotkeyID      = 1
	wmHotkey      = 0x0312
	wmReHotkey    = 0x8000 + 1 // WM_APP+1：配置变化后重新注册
	cfUnicodeText = 13
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procSendInput        = user32.NewProc("SendInput")
	procClipboardSeq     = user32.NewProc("GetClipboardSequenceNumber")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
)

// msg 对应 Win32 MSG 结构（x64 下按 8 字节对齐补足 48 字节）。
type msg struct {
	hwnd    uintptr
	message uint32
	_       uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      [2]int32
	_       uint32
}

// startHotkeyWatcher 在锁定线程上建一个 message-only 窗口接收 WM_HOTKEY，
// 应用生命周期内常驻；热键注册随进程退出自动失效。
func (a *App) startHotkeyWatcher() {
	goruntime.LockOSThread()

	cls, err := syscall.UTF16PtrFromString("STATIC")
	if err != nil {
		return
	}
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0,
		^uintptr(2), // HWND_MESSAGE：不可见的消息窗口
		0, 0, 0)
	if hwnd == 0 {
		return
	}
	a.hotkeyWnd.Store(hwnd)
	a.reghotkey()

	var m msg
	for {
		n, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(n) <= 0 {
			return
		}
		switch m.message {
		case wmHotkey:
			a.onHotkey()
		case wmReHotkey:
			a.reghotkey()
		}
	}
}

// reghotkey 按当前配置注册热键；停用或组合键解析失败时不注册。
func (a *App) reghotkey() {
	hwnd := a.hotkeyWnd.Load()
	if hwnd == 0 {
		return
	}
	procUnregisterHotKey.Call(hwnd, hotkeyID)
	a.mu.Lock()
	combo, off := a.config.Hotkey, a.config.HotkeyOff
	a.mu.Unlock()
	if off {
		return
	}
	if mod, vk, ok := parseHotkey(combo); ok {
		procRegisterHotKey.Call(hwnd, hotkeyID, uintptr(mod), uintptr(vk))
	}
}

// applyHotkey 通知 watcher 线程按新配置重新注册（可在任意 goroutine 调用）。
func (a *App) applyHotkey() {
	if hwnd := a.hotkeyWnd.Load(); hwnd != 0 {
		procPostMessageW.Call(hwnd, wmReHotkey, 0, 0)
	}
}

// onHotkey 模拟 Ctrl+C 抓取当前选中文本，唤起主窗口并通知前端翻译。
func (a *App) onHotkey() {
	seq0, _, _ := procClipboardSeq.Call()
	sendCopy()

	text := ""
	for deadline := time.Now().Add(600 * time.Millisecond); ; {
		time.Sleep(30 * time.Millisecond)
		if seq, _, _ := procClipboardSeq.Call(); seq != seq0 {
			text = readClipboardText()
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	text = strings.TrimSpace(text)

	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
	runtime.EventsEmit(a.ctx, "quick:translate", map[string]string{"text": text})
}

// keybdInput 对应 Win32 INPUT 结构的 Keyboard 分支；联合体按最大的
// MOUSEINPUT 补齐大小，否则 SendInput 会因 cbSize 不符而拒绝调用。
type keybdInput struct {
	typ   uint32
	_     uint32
	wVk   uint16
	wScan uint16
	flags uint32
	time  uint32
	extra uintptr
	_     [8]byte
}

// sendCopy 发送“松开其他修饰键 + Ctrl+C”。快捷键若含 Shift/Alt，先合成
// 松开，避免目标程序把按键理解成 Ctrl+Shift+C 之类的其他组合。
func sendCopy() {
	const (
		inputKeyboard = 1
		keyUp         = 2 // KEYEVENTF_KEYUP
		vkShift       = 0x10
		vkControl     = 0x11
		vkMenu        = 0x12
		vkLWin        = 0x5B
		vkRWin        = 0x5C
		vkC           = 0x43
	)
	down := func(vk uint32) keybdInput { return keybdInput{typ: inputKeyboard, wVk: uint16(vk)} }
	up := func(vk uint32) keybdInput { return keybdInput{typ: inputKeyboard, wVk: uint16(vk), flags: keyUp} }
	inputs := []keybdInput{
		up(vkShift), up(vkMenu), up(vkLWin), up(vkRWin), up(vkControl),
		down(vkControl), down(vkC), up(vkC),
	}
	procSendInput.Call(
		uintptr(len(inputs)),
		uintptr(unsafe.Pointer(&inputs[0])),
		unsafe.Sizeof(keybdInput{}))
}

// readClipboardText 读取剪贴板纯文本（CF_UNICODETEXT）；剪贴板正被其他
// 程序占用时稍等重试，最终失败返回空串。
func readClipboardText() string {
	for i := 0; i < 10; i++ {
		if r, _, _ := procOpenClipboard.Call(0); r != 0 {
			text := readClipboardData()
			procCloseClipboard.Call()
			return text
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

func readClipboardData() string {
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer procGlobalUnlock.Call(h)

	// 上限 1M 字符，实际以 NUL 截断
	s := unsafe.Slice((*uint16)(ptrTo(p)), 1<<20)
	n := 0
	for n < len(s) && s[n] != 0 {
		n++
	}
	return string(utf16.Decode(s[:n]))
}

// ptrTo 把 syscall 返回的地址转回指针。该内存由系统分配，与 Go GC 无关，
// 且只在剪贴板锁定期间同步访问。
func ptrTo(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

var vkNames = map[string]uint32{
	"space": 0x20, "enter": 0x0D, "return": 0x0D, "tab": 0x09, "backspace": 0x08,
	"up": 0x26, "down": 0x28, "left": 0x25, "right": 0x27,
	"home": 0x24, "end": 0x23,
	"pgup": 0x21, "pgdn": 0x22, "pageup": 0x21, "pagedown": 0x22,
	"insert": 0x2D, "delete": 0x2E, "del": 0x2E,
}

// parseHotkey 解析 "Ctrl+Shift+Space" 形式的组合键，必须带修饰键。
func parseHotkey(s string) (mod, vk uint32, ok bool) {
	for _, part := range strings.Split(strings.TrimSpace(s), "+") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "", "off":
			return 0, 0, false
		case "ctrl", "control":
			mod |= 2 // MOD_CONTROL
		case "alt":
			mod |= 1 // MOD_ALT
		case "shift":
			mod |= 4 // MOD_SHIFT
		case "win", "meta", "super":
			mod |= 8 // MOD_WIN
		default:
			if vk != 0 {
				return 0, 0, false // 出现两个主键
			}
			if vk = vkOf(part); vk == 0 {
				return 0, 0, false
			}
		}
	}
	return mod, vk, vk != 0 && mod != 0
}

// vkOf 把单个主键名转成虚拟键码：字母、数字、F1-F24 及若干命名键。
func vkOf(part string) uint32 {
	p := strings.ToUpper(strings.TrimSpace(part))
	if len(p) == 1 {
		if c := p[0]; (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			return uint32(c)
		}
		return 0
	}
	if len(p) <= 3 && p[0] == 'F' { // F1-F24
		n := 0
		for _, d := range p[1:] {
			if d < '0' || d > '9' {
				return 0
			}
			n = n*10 + int(d-'0')
		}
		if n >= 1 && n <= 24 {
			return 0x70 + uint32(n) - 1
		}
		return 0
	}
	return vkNames[strings.ToLower(p)]
}
