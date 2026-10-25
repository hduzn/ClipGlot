package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ProviderStatus 告知前端本次翻译启动了哪些服务。
type ProviderStatus struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

// TranslateStart 是 TranslateAll 的返回值。
type TranslateStart struct {
	Seq       int              `json:"seq"`
	Providers []ProviderStatus `json:"providers"`
}

type App struct {
	ctx    context.Context
	config *Config

	mu     sync.Mutex
	seq    int                // 翻译轮次号，旧轮次的事件会被前端丢弃
	cancel context.CancelFunc // 取消进行中的翻译
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.config = LoadConfig()
}

// onSecondInstance 再次启动时把已有窗口带到前台。
func (a *App) onSecondInstance(_ options.SecondInstanceData) {
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

// GetConfig 返回完整配置（本地应用，含 API Key）。
func (a *App) GetConfig() *Config { return a.config }

// SaveConfig 覆盖保存设置，返回保存后的配置。
func (a *App) SaveConfig(cfg Config) (*Config, error) {
	clean := make([]ProviderConfig, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		p.Name = strings.TrimSpace(p.Name)
		p.Model = strings.TrimSpace(p.Model)
		p.BaseURL = strings.TrimSpace(p.BaseURL)
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.Type != "preset" && p.Type != "custom" {
			p.Type = "custom"
		}
		if p.ID == "" {
			p.ID = newCustomID()
		}
		if p.Name == "" {
			p.Name = "自定义服务"
		}
		clean = append(clean, p)
	}
	cfg.Providers = clean
	if cfg.TargetLang == "" {
		cfg.TargetLang = "en"
	}
	a.config = &cfg
	if err := saveConfigFile(a.config); err != nil {
		return a.config, err
	}
	return a.config, nil
}

// SetTargetLang 记住目标语言选择。
func (a *App) SetTargetLang(code string) {
	a.config.TargetLang = code
	_ = saveConfigFile(a.config)
}

// SetTheme 切换界面主题：system 跟随系统 / light 浅色 / dark 深色。
func (a *App) SetTheme(theme string) {
	if theme != "light" && theme != "dark" {
		theme = "system"
	}
	a.config.Theme = theme
	_ = saveConfigFile(a.config)
}

// TranslateAll 把文本并发发给所有已启用的服务，流式结果通过事件返回：
// translate:chunk / translate:done / translate:error，载荷都带 seq 轮次号。
func (a *App) TranslateAll(text, target string) TranslateStart {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	a.cancel = cancel
	a.seq++
	start := TranslateStart{Seq: a.seq}
	cfg := a.config
	a.mu.Unlock()

	for _, p := range cfg.Providers {
		if !p.Enabled || p.APIKey == "" || p.Model == "" {
			continue
		}
		start.Providers = append(start.Providers, ProviderStatus{ID: p.ID, Name: p.Name, Model: p.Model})
		go a.translateOne(ctx, start.Seq, p, text, target)
	}
	return start
}

func (a *App) translateOne(ctx context.Context, seq int, p ProviderConfig, text, target string) {
	var (
		mu  sync.Mutex
		buf strings.Builder
	)
	flush := func() {
		mu.Lock()
		s := buf.String()
		buf.Reset()
		mu.Unlock()
		if s != "" {
			runtime.EventsEmit(a.ctx, "translate:chunk", map[string]any{"seq": seq, "id": p.ID, "text": s})
		}
	}

	// 每 60ms 合并一次增量再发给前端，避免长文本刷爆事件通道
	ticker := time.NewTicker(60 * time.Millisecond)
	defer ticker.Stop()
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				flush()
			}
		}
	}()

	err := streamTranslate(ctx, p, text, target, func(delta string) {
		mu.Lock()
		buf.WriteString(delta)
		mu.Unlock()
	})
	close(done)
	flush()

	switch {
	case ctx.Err() != nil:
		// 已被新一轮翻译取消，前端早已重置，无需上报
	case err != nil:
		runtime.EventsEmit(a.ctx, "translate:error", map[string]any{"seq": seq, "id": p.ID, "message": err.Error()})
	default:
		runtime.EventsEmit(a.ctx, "translate:done", map[string]any{"seq": seq, "id": p.ID})
	}
}
