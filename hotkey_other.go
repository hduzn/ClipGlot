//go:build !windows

package main

// 非 Windows 平台暂无全局热键实现（应用本身面向 Windows）。

func (a *App) startHotkeyWatcher() {}

func (a *App) applyHotkey() {}
