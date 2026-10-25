package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ProviderConfig 描述一个 AI 翻译服务：preset 为内置服务，
// custom 为用户添加的任意 OpenAI 兼容服务。
type ProviderConfig struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // "preset" | "custom"
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Enabled bool   `json:"enabled"`
}

// Config 是应用的全部持久化状态。
type Config struct {
	Providers  []ProviderConfig `json:"providers"`
	TargetLang string           `json:"targetLang"`
	Theme      string           `json:"theme"`  // "system" | "light" | "dark"
	Hotkey     string           `json:"hotkey"` // 划词翻译全局快捷键，如 "Ctrl+Shift+Space"
	HotkeyOff  bool             `json:"hotkeyOff"`
}

func DefaultConfig() *Config {
	return &Config{
		TargetLang: "en",
		Theme:      "system",
		Hotkey:     "Alt+T",
		Providers: []ProviderConfig{
			{ID: "bailian", Type: "preset", Name: "阿里云百炼", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Model: "qwen-mt-flash"},
			{ID: "deepseek", Type: "preset", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-flash"},
		},
	}
}

// configPaths 返回配置文件的候选位置：优先与 exe 同目录（便携模式），
// exe 所在目录不可写时（如放在受保护的安装目录）退回 %AppData%\ClipGlot。
func configPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "config.json"))
	}
	if dir, err := os.UserConfigDir(); err == nil {
		dir = filepath.Join(dir, "ClipGlot")
		_ = os.MkdirAll(dir, 0o700)
		paths = append(paths, filepath.Join(dir, "config.json"))
	}
	return paths
}

// LoadConfig 按候选顺序读取配置；首次运行或文件损坏时返回默认配置，
// 并保证内置服务始终存在。从 %AppData% 旧位置读到时会自动迁移到 exe 目录。
func LoadConfig() *Config {
	cfg := DefaultConfig()
	paths := configPaths()
	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var saved Config
		if json.Unmarshal(data, &saved) != nil {
			continue
		}
		applySaved(cfg, saved)
		if i > 0 { // 旧位置 → 便携位置
			if merged, err := json.MarshalIndent(cfg, "", "  "); err == nil {
				if err := os.WriteFile(paths[0], merged, 0o600); err == nil {
					_ = os.Remove(path)
				}
			}
		}
		return cfg
	}
	return cfg
}

// applySaved 把已保存的配置合并进默认配置：preset 按 ID 覆盖，custom 追加在后。
func applySaved(cfg *Config, saved Config) {
	if saved.TargetLang != "" {
		cfg.TargetLang = saved.TargetLang
	}
	if saved.Theme != "" {
		cfg.Theme = saved.Theme
	}
	if saved.Hotkey != "" {
		cfg.Hotkey = saved.Hotkey
	}
	cfg.HotkeyOff = saved.HotkeyOff
	var customs []ProviderConfig
	for _, p := range saved.Providers {
		if p.Type == "custom" {
			customs = append(customs, p)
			continue
		}
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == p.ID {
				cfg.Providers[i] = p
				break
			}
		}
	}
	cfg.Providers = append(cfg.Providers, customs...)
}

func saveConfigFile(cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	var lastErr error
	for _, path := range configPaths() {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			lastErr = err
			continue
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func newCustomID() string {
	return "custom-" + time.Now().Format("0102150405")
}
