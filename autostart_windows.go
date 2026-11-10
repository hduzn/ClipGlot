//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// 开机启动：便携软件的标准做法是往 HKCU 的 Run 键写一条指向当前 exe 的
// 带引号绝对路径，取消勾选时删除该值。不进 config.json——注册表本身就是
// 持久层，配置文件再存一份状态容易和真实情况打架。
const (
	autostartKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValue   = "ClipGlot"
)

// autostartEnabled 读注册表判断开机启动是否已勾选。
func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValue)
	return err == nil && v != ""
}

// setAutoStart 开启时写入 Run 键（每次都重写，exe 换位置后自动指向新位置），
// 关闭时删除；值本来就不存在时删除报 ErrNotExist，不算失败。
func setAutoStart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(autostartValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	p, err := os.Executable()
	if err != nil {
		return err
	}
	// 路径带引号：exe 放在含空格的目录（如 Program Files）也能正确启动
	return k.SetStringValue(autostartValue, `"`+p+`"`)
}

// syncAutoStartPath 在已勾选开机启动但 exe 换了位置时，静默把注册表里的
// 路径刷新为当前 exe，避免移动/重命名 exe 后启动项悄悄失效。
func syncAutoStartPath() {
	if !autostartEnabled() {
		return
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	old, _, err := k.GetStringValue(autostartValue)
	if err != nil {
		return
	}
	p, err := os.Executable()
	if err != nil || old == `"`+p+`"` {
		return
	}
	_ = setAutoStart(true)
}
