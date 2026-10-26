//go:build windows

package main

// Windows 后台能力：全局划词快捷键 + 系统托盘。
// 主窗口关闭/最小化只是隐藏，程序驻留托盘，快捷键随时可用；
// 托盘左键唤起主窗口，右键菜单可显示主窗口或退出。

import (
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	hotkeyID    = 1 // 热键与托盘图标共用 id=1（各自独立命名空间）
	wmHotkey    = 0x0312
	wmReHotkey  = 0x8000 + 1 // WM_APP+1：按新配置重注册热键
	wmTrayIcon  = 0x8000 + 2 // NOTIFYICONDATA.uCallbackMessage
	wmCommand   = 0x0111
	trayCmdShow = 1
	trayCmdQuit = 2

	whKeyboardLl  = 13 // WH_KEYBOARD_LL：双击 Ctrl+C 手势用的低级键盘钩子
	wmKeyDown     = 0x0100
	wmKeyUp       = 0x0101
	wmSysKeyDown  = 0x0104
	wmSysKeyUp    = 0x0105
	llkhfInjected = 0x10 // LLKHF_INJECTED：SendInput 等注入的按键
	vkKeyC        = 0x43
	vkControl     = 0x11 // 通用 Ctrl（不分左右），GetAsyncKeyState 查实时键态用
	vkLControl    = 0xA2
	vkRControl    = 0xA3

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifMsg    = 1 // NIF_MESSAGE
	nifIcon   = 2 // NIF_ICON
	nifTip    = 4 // NIF_TIP
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procRegisterHotKey         = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey       = user32.NewProc("UnregisterHotKey")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procSetWindowsHookExW      = user32.NewProc("SetWindowsHookExW")
	procCallNextHookEx         = user32.NewProc("CallNextHookEx")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procGetModuleFileNameW     = kernel32.NewProc("GetModuleFileNameW")

	procSendInput        = user32.NewProc("SendInput")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	procClipboardSeq     = user32.NewProc("GetClipboardSequenceNumber")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")

	procShellNotifyIcon  = shell32.NewProc("Shell_NotifyIconW")
	procExtractIconExW   = shell32.NewProc("ExtractIconExW")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenuEx = user32.NewProc("TrackPopupMenuEx")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procSetForegroundWin      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcID = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput     = user32.NewProc("AttachThreadInput")
	procFindWindowW           = user32.NewProc("FindWindowW")
	procIsIconic              = user32.NewProc("IsIconic")
	procShowWindow            = user32.NewProc("ShowWindow")
	procLoadIconW             = user32.NewProc("LoadIconW")
	procGetCurrentThreadId    = kernel32.NewProc("GetCurrentThreadId")
)

var (
	appRef            atomic.Pointer[App] // 窗口过程运行在 watcher 线程，需要拿到 App
	taskbarCreatedMsg atomic.Uint32       // explorer 重启后的广播消息，收到后重建托盘图标
	trayWndProcPtr    = syscall.NewCallback(func(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
		if a := appRef.Load(); a != nil {
			switch msg {
			case wmHotkey:
				// lParam 携带钩子在手势识别瞬间记录的剪贴板序号（0=未知，现场取）。
				// 放到 goroutine 跑：onHotkey 最长等剪贴板 600ms，不能卡住消息循环，
				// 否则低级键盘钩子回调被拖到超时，双击手势会失灵
				go a.onHotkey(uintptr(lParam))
				return 0
			case wmReHotkey:
				a.reghotkey()
				return 0
			case wmTrayIcon:
				switch uint32(lParam) {
				case 0x0202, 0x0203: // 左键单击/双击：唤起主窗口
					a.showWindow()
				case 0x0205: // 右键：弹出菜单
					a.showTrayMenu(hwnd)
				}
				return 0
			case wmCommand:
				switch uint32(wParam & 0xFFFF) {
				case trayCmdShow:
					a.showWindow()
				case trayCmdQuit:
					a.quit()
				}
				return 0
			default:
				if v := taskbarCreatedMsg.Load(); v != 0 && msg == v {
					a.addTrayIcon(hwnd, nimAdd) // explorer 重启后重建图标
				}
			}
		}
		r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
		return r
	})
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

// kbdllHookStruct 对应 Win32 KBDLLHOOKSTRUCT（x64 共 24 字节）。
type kbdllHookStruct struct {
	vkCode   uint32
	scanCode uint32
	flags    uint32
	time     uint32
	extra    uintptr
}

// hookProcPtr 低级键盘钩子：只做“双击 Ctrl+C”手势识别，极轻量、永不吞键。
var hookProcPtr = syscall.NewCallback(func(nCode, wParam, lParam uintptr) uintptr {
	if nCode >= 0 {
		if a := appRef.Load(); a != nil {
			a.hookKey(uint32(wParam), (*kbdllHookStruct)(ptrTo(lParam)))
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, nCode, wParam, lParam)
	return r
})

// wndClassEx 对应 Win32 WNDCLASSEXW 结构。
type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  uintptr
	lpszClassName uintptr
	hIconSm       uintptr
}

// startHotkeyWatcher 在锁定线程上创建隐藏窗口：接收热键与托盘消息，
// 挂托盘图标，并启动最小化转隐藏的轮询。应用生命周期内常驻。
func (a *App) startHotkeyWatcher() {
	goruntime.LockOSThread()
	appRef.Store(a)

	hInstance, _, _ := procGetModuleHandleW.Call(0)
	clsName, _ := syscall.UTF16PtrFromString("ClipGlotTray")
	cls := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   trayWndProcPtr,
		hInstance:     hInstance,
		lpszClassName: uintptr(unsafe.Pointer(clsName)),
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls)))

	// 顶层隐藏窗口（不设父窗口）：托盘图标回调与 TaskbarCreated 广播
	// 都需要真实顶层窗口，message-only 窗口收不到广播
	hwnd, _, _ := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(clsName)), 0, 0, 0, 0, 0, 0,
		0, 0, hInstance, 0)
	if hwnd == 0 {
		return
	}
	a.hotkeyWnd.Store(hwnd)

	s, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	if v, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(s))); v != 0 {
		taskbarCreatedMsg.Store(uint32(v))
	}

	a.reghotkey()
	a.trayMenu = createTrayMenu()
	a.addTrayIcon(hwnd, nimAdd)
	a.installDoubleCopyHook(hInstance)
	go a.hideOnMinimise()

	var m msg
	for {
		n, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(n) <= 0 {
			return
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// reghotkey 按当前配置注册热键；停用或组合键解析失败时不注册，
// 同时把托盘悬停提示同步为当前快捷键。
func (a *App) reghotkey() {
	hwnd := a.hotkeyWnd.Load()
	if hwnd == 0 {
		return
	}
	procUnregisterHotKey.Call(hwnd, hotkeyID)
	a.mu.Lock()
	combo, off, doubleOff := a.config.Hotkey, a.config.HotkeyOff, a.config.DoubleCopyOff
	a.mu.Unlock()
	a.doubleCopyOff.Store(doubleOff)
	if !off {
		if mod, vk, ok := parseHotkey(combo); ok {
			procRegisterHotKey.Call(hwnd, hotkeyID, uintptr(mod), uintptr(vk))
		}
	}
	a.addTrayIcon(hwnd, nimModify)
}

// installDoubleCopyHook 装低级键盘钩子，识别“双击 Ctrl+C”手势。
// 回调运行在 watcher 线程的消息循环里，只记状态和发消息，不吞任何键。
func (a *App) installDoubleCopyHook(hInstance uintptr) {
	procSetWindowsHookExW.Call(whKeyboardLl, hookProcPtr, hInstance, 0)
}

// hookKey 识别“双击 Ctrl+C”：700ms 内按住 Ctrl 连按两次 C，中间没碰过别的键。
// 识别到后发一条 wmHotkey 复用现有翻译流程；所有按键原样放行，正常 Ctrl+C
// 不受影响；按住 C 产生的自动重复不算第二击；Ctrl 状态用 GetAsyncKeyState
// 实时校准，钩子事件被系统丢弃时手势依然可识别。
func (a *App) hookKey(msg uint32, k *kbdllHookStruct) {
	if a.doubleCopyOff.Load() || k.flags&llkhfInjected != 0 {
		return // 功能关闭；忽略 SendInput 等注入的按键，避免自触发
	}
	isCtrl := k.vkCode == vkLControl || k.vkCode == vkRControl
	switch msg {
	case wmKeyDown, wmSysKeyDown:
		if isCtrl {
			a.dcCtrlDown = true
			return
		}
		if k.vkCode == vkKeyC {
			now := time.Now()
			// 按住 C 不放产生的自动重复间隔只有几十毫秒，远小于人手连击；
			// 若 C 的 up 事件被系统丢弃，仅靠 dcCDown 会把真实第二击误当
			// 自动重复，这里加时间窗双重判断兜底
			if a.dcCDown && now.Sub(a.dcLastC) < 250*time.Millisecond {
				return
			}
			a.dcCDown = true
			// 以实时键态校准 Ctrl：低级钩子事件在系统繁忙时可能被超时丢弃，
			// 只靠 down/up 序列维护状态会误判（双击时灵时不灵的主因）
			if r, _, _ := procGetAsyncKeyState.Call(uintptr(vkControl)); r&0x8000 == 0 {
				a.dcCtrlDown = false
				a.dcLastC = time.Time{} // 不带 Ctrl 的 C 不构成手势
				return
			}
			a.dcCtrlDown = true
			if !a.dcLastC.IsZero() && now.Sub(a.dcLastC) < 700*time.Millisecond {
				a.dcLastC = time.Time{}
				if hwnd := a.hotkeyWnd.Load(); hwnd != 0 {
					// 双击！此刻按键尚未放行，目标程序还没执行复制，先记下
					// 剪贴板序号，供 onHotkey 作为比对基线（随 lParam 传回）
					seq, _, _ := procClipboardSeq.Call()
					procPostMessageW.Call(hwnd, wmHotkey, 0, seq)
				}
				return
			}
			a.dcLastC = now
			return
		}
		a.dcLastC = time.Time{} // 两次 C 之间按了别的键，序列作废
	case wmKeyUp, wmSysKeyUp:
		if isCtrl {
			a.dcCtrlDown = false
		}
		if k.vkCode == vkKeyC {
			a.dcCDown = false
		}
	}
}

// notifyIconData 对应 Win32 NOTIFYICONDATAW 结构（x64 共 984 字节）。
type notifyIconData struct {
	cbSize           uint32
	_                uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	_                uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	_                uint32
	hBalloonIcon     uintptr
}

// addTrayIcon 添加（nimAdd）或更新提示（nimModify）托盘图标。
func (a *App) addTrayIcon(hwnd uintptr, op uint32) {
	if op == nimModify && hwnd == 0 {
		return
	}
	if a.trayIcon == 0 {
		a.trayIcon = appIcon()
		if a.trayIcon == 0 {
			return
		}
	}
	a.mu.Lock()
	tip := "ClipGlot"
	var triggers []string
	if !a.config.HotkeyOff {
		triggers = append(triggers, a.config.Hotkey)
	}
	if !a.config.DoubleCopyOff {
		triggers = append(triggers, "双击 Ctrl+C")
	}
	if len(triggers) > 0 {
		tip = "ClipGlot · " + strings.Join(triggers, " / ") + " 划词翻译"
	}
	a.mu.Unlock()

	var nid notifyIconData
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	nid.hWnd = hwnd
	nid.uID = hotkeyID
	nid.uFlags = nifMsg | nifIcon | nifTip
	nid.uCallbackMessage = wmTrayIcon
	nid.hIcon = a.trayIcon
	tipUTF16 := utf16.Encode([]rune(tip))
	copy(nid.szTip[:127], tipUTF16) // 留一个位置给 NUL，数组本身已零初始化
	procShellNotifyIcon.Call(uintptr(op), uintptr(unsafe.Pointer(&nid)))
}

// createTrayMenu 建托盘右键菜单：显示主窗口 / 退出。
func createTrayMenu() uintptr {
	m, _, _ := procCreatePopupMenu.Call()
	showText, _ := syscall.UTF16PtrFromString("显示主窗口")
	quitText, _ := syscall.UTF16PtrFromString("退出")
	procAppendMenuW.Call(m, 0 /*MF_STRING*/, trayCmdShow, uintptr(unsafe.Pointer(showText)))
	procAppendMenuW.Call(m, 0x800 /*MF_SEPARATOR*/, 0, 0)
	procAppendMenuW.Call(m, 0 /*MF_STRING*/, trayCmdQuit, uintptr(unsafe.Pointer(quitText)))
	return m
}

// showTrayMenu 弹出托盘右键菜单。TrackPopupMenu 前先 SetForegroundWindow
// 是菜单无法消失的经典修法（MS KB135788）；TPM_RETURNCMD 直接返回选中项。
func (a *App) showTrayMenu(hwnd uintptr) {
	var pt [2]int32
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt[0])))
	procSetForegroundWin.Call(hwnd)
	id, _, _ := procTrackPopupMenuEx.Call(
		a.trayMenu, 0x0100|0x0002, /* TPM_RETURNCMD | TPM_RIGHTBUTTON */
		uintptr(pt[0]), uintptr(pt[1]), hwnd, 0)
	procPostMessageW.Call(hwnd, 0 /*WM_NULL*/, 0, 0)
	switch id {
	case trayCmdShow:
		a.showWindow()
	case trayCmdQuit:
		a.quit()
	}
}

// quit 退出程序：先摘掉托盘图标再结束，避免图标残留到鼠标划过才消失。
func (a *App) quit() {
	if hwnd := a.hotkeyWnd.Load(); hwnd != 0 {
		var nid notifyIconData
		nid.cbSize = uint32(unsafe.Sizeof(nid))
		nid.hWnd = hwnd
		nid.uID = hotkeyID
		procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	}
	runtime.Quit(a.ctx)
}

// clipglotTitle 主窗口标题，供 FindWindow 用；包级变量避免轮询里反复分配。
var clipglotTitle, _ = syscall.UTF16PtrFromString("ClipGlot")

// forceForeground 把主窗口强制带到前台。注册热键触发时系统会授予前台
// 权限，但双击 Ctrl+C 走钩子路径没有该权限，后台进程直接
// SetForegroundWindow 会被前台锁定策略拦下（窗口只在任务栏闪烁）；
// 短暂 AttachThreadInput 到当前前台线程共享输入状态后再置前即可绕过。
func (a *App) forceForeground() {
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(clipglotTitle)))
	if h == 0 {
		return
	}
	thisThread, _, _ := procGetCurrentThreadId.Call()
	fg, _, _ := procGetForegroundWindow.Call()
	fgThread, _, _ := procGetWindowThreadProcID.Call(fg, 0)
	attached := fg != 0 && fgThread != 0 && fgThread != thisThread
	if attached {
		procAttachThreadInput.Call(thisThread, fgThread, 1)
	}
	procSetForegroundWin.Call(h)
	if attached {
		procAttachThreadInput.Call(thisThread, fgThread, 0)
	}
}

// hideOnMinimise 把“最小化”变成“隐藏到托盘”。Wails v2 没有最小化拦截钩子，
// 这里轮询主窗口状态，发现最小化立即隐藏；150ms 的轮询开销可忽略。
func (a *App) hideOnMinimise() {
	for {
		time.Sleep(150 * time.Millisecond)
		h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(clipglotTitle)))
		if h == 0 {
			continue
		}
		if iconic, _, _ := procIsIconic.Call(h); iconic != 0 {
			procShowWindow.Call(h, 0 /*SW_HIDE*/)
		}
	}
}

// appIcon 取应用图标：优先从自身 exe 提取（Wails 已把 build/appicon.png
// 编译进资源），失败退回系统默认图标。
func appIcon() uintptr {
	path := make([]uint16, 32768)
	procGetModuleFileNameW.Call(0, uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)))
	var h uintptr
	procExtractIconExW.Call(
		uintptr(unsafe.Pointer(&path[0])), 0,
		uintptr(unsafe.Pointer(&h)), 0, 1)
	if h != 0 {
		return h
	}
	h, _, _ = procLoadIconW.Call(0, 32512) // IDI_APPLICATION
	return h
}

// applyHotkey 通知 watcher 线程按新配置重新注册（可在任意 goroutine 调用）。
func (a *App) applyHotkey() {
	if hwnd := a.hotkeyWnd.Load(); hwnd != 0 {
		procPostMessageW.Call(hwnd, wmReHotkey, 0, 0)
	}
}

// onHotkey 模拟 Ctrl+C 抓取当前选中文本，唤起主窗口并通知前端翻译。
// seq0 是触发瞬间的剪贴板序号：双击 Ctrl+C 路径由钩子在按键放行前记录，
// 用户真实的第二次 Ctrl+C 随即就把选中文本写入了剪贴板，比对该序号能直接
// 读到文本，不依赖目标程序响应合成按键（不少程序复制相同内容时不重写
// 剪贴板，序号不变）；全局热键路径触发时用户尚未复制，传 0 由这里现场取。
func (a *App) onHotkey(seq0 uintptr) {
	if seq0 == 0 {
		seq0, _, _ = procClipboardSeq.Call()
	}
	sendCopy()
	a.showWindow() // 先唤起窗口，抓取文本与显示并行，体感更快

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
	runtime.EventsEmit(a.ctx, "quick:translate", map[string]string{"text": strings.TrimSpace(text)})
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

// sendCopy 发送“松开仍被按住的修饰键 + Ctrl+C”。快捷键若含 Shift/Alt，先合成
// 松开，避免目标程序把按键理解成 Ctrl+Shift+C 之类的其他组合。只对真正按下
// 的修饰键发合成 key-up：多余的事件会成为“无配对按下”的孤儿事件，干扰输入法
// 和依赖修饰键状态的程序（按 Alt+T 后键盘失灵的经典成因）。
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
	var inputs []keybdInput
	for _, vk := range []uint32{vkShift, vkMenu, vkLWin, vkRWin, vkControl} {
		if r, _, _ := procGetAsyncKeyState.Call(uintptr(vk)); r&0x8000 != 0 { // 高位=当前按下
			inputs = append(inputs, up(vk))
		}
	}
	inputs = append(inputs, down(vkControl), down(vkC), up(vkC))
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

const cfUnicodeText = 13

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
