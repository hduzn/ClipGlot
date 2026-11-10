//go:build windows

package main

import "testing"

// 常用短语的快捷键必须能解析出「带修饰键 + 单个主键」，且不允许只有修饰键。
func TestParseSnippetHotkey(t *testing.T) {
	mod, vk, ok := parseHotkey("Ctrl+Shift+1")
	if !ok || mod != 6 /* MOD_CONTROL|MOD_SHIFT */ || vk != 0x31 {
		t.Fatalf("Ctrl+Shift+1 解析结果不对：mod=%d vk=0x%X ok=%v", mod, vk, ok)
	}
	if _, _, ok := parseHotkey("Ctrl+Shift"); ok {
		t.Fatal("只有修饰键的组合不应注册")
	}
	if _, _, ok := parseHotkey("Ctrl+Shift+A+B"); ok {
		t.Fatal("两个主键的组合不应注册")
	}
	if _, _, ok := parseHotkey("1"); ok {
		t.Fatal("无修饰键的组合不应注册")
	}
}

// 托盘菜单里的名字：优先用名称，没名称退化到内容开头，并做长度截断。
func TestSnippetLabel(t *testing.T) {
	if got := snippetLabel(Snippet{Name: "设计风格", Text: "随便什么"}); got != "设计风格" {
		t.Fatalf("有名称时应直接用名称，得到 %q", got)
	}
	long := "设计风格参考 awwwards 获奖作品，注重排版、字体层级与留白，整体克制。"
	if got := snippetLabel(Snippet{Text: long}); len([]rune(got)) != snippetMenuMax+1 {
		t.Fatalf("名称缺失时应截断内容，得到 %q", got)
	}
	if got := snippetLabel(Snippet{}); got == "" {
		t.Fatal("空条目也应有个占位名字")
	}
}

// 剪贴板写入是粘贴功能的关键路径，做一次写入/读回校验；结束时还原原内容。
// 剪贴板是全局资源，会被剪贴板历史/远程桌面等程序占用，-short 时可跳过。
func TestClipboardRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实剪贴板读写（-short）")
	}
	old := readClipboardText()
	defer func() {
		if old != "" {
			writeClipboardText(old)
		}
	}()

	const want = "设计风格参考 awwwards 获奖作品，注重排版。\n第二行"
	if !writeClipboardText(want) {
		t.Fatal("写入剪贴板失败")
	}
	if got := readClipboardText(); got != want {
		t.Fatalf("读回内容不一致：\nwant=%q\ngot =%q", want, got)
	}
}
