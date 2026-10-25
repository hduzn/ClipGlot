package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func translationPrompt(target string) string {
	return "You are a professional translation engine. Translate the user's text into " + target + "." +
		" Auto-detect the source language." +
		" Output ONLY the translation — no explanations, notes or surrounding quotes." +
		" Preserve the original formatting, line breaks and punctuation style." +
		" Keep proper nouns, code identifiers and technical terms unchanged." +
		" If the text is already in the target language, output it unchanged."
}

// qwenMTTarget 把前端的英文语言名映射为 Qwen-MT 要求的 target_lang 写法。
func qwenMTTarget(en string) string {
	if en == "Simplified Chinese" {
		return "Chinese" // Qwen-MT 用 "Chinese" 表示简体中文
	}
	return en // 其余（English、Japanese、Traditional Chinese…）与 Qwen-MT 命名一致
}

// buildPayload 组装请求体。Qwen-MT 系列是翻译专用模型，调用方式与普通
// 聊天模型不同：仅允许一条 user 消息、不支持 System Message，
// 语种通过 translation_options 传入（source_lang=auto 即自动检测）。
func buildPayload(p ProviderConfig, text, target string) ([]byte, error) {
	if strings.HasPrefix(strings.ToLower(p.Model), "qwen-mt") {
		return json.Marshal(map[string]any{
			"model":    p.Model,
			"messages": []chatMessage{{Role: "user", Content: text}},
			"stream":   true,
			"translation_options": map[string]string{
				"source_lang": "auto",
				"target_lang": qwenMTTarget(target),
			},
		})
	}
	return json.Marshal(map[string]any{
		"model": p.Model,
		"messages": []chatMessage{
			{Role: "system", Content: translationPrompt(target)},
			{Role: "user", Content: text},
		},
		"stream":      true,
		"temperature": 0.2,
	})
}

// completionsURL 把用户填写的 BaseURL 规整为 .../chat/completions。
func completionsURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// streamTranslate 调用 OpenAI 兼容的 /chat/completions 接口（SSE 流式），
// 每收到一段增量文本就回调 onDelta。推理类模型输出的 reasoning_content 会被自动忽略。
func streamTranslate(ctx context.Context, p ProviderConfig, text, target string, onDelta func(string)) error {
	payload, err := buildPayload(p, text, target)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, completionsURL(p.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("无法连接 %s：%v", hostOf(p.BaseURL), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return apiError(resp.StatusCode, body)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	got := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return errors.New(chunk.Error.Message)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			got = true
			onDelta(chunk.Choices[0].Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil && !got {
		return err
	}
	return nil
}

// statusHint 把常见 HTTP 状态码翻译成可操作的中文提示。
func statusHint(status int) string {
	switch status {
	case 401:
		return "API Key 无效或未授权"
	case 402:
		return "账户余额不足，请充值或改用该平台的免费模型"
	case 403:
		return "无权访问该模型（可能需要实名认证或账户余额）"
	case 404:
		return "服务地址或模型名不存在"
	case 429:
		return "请求过于频繁或超出额度限制"
	case 500, 502, 503:
		return "服务端暂时出错，请稍后重试"
	}
	return ""
}

// apiError 尽力从错误响应里提取人话：
// 兼容 {"error":{"message":…}}（OpenAI/百炼/DeepSeek/智谱）
// 和 {"code":…,"message":…}（硅基流动等扁平格式）两种结构。
func apiError(status int, body []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		msg := e.Error.Message
		if msg == "" {
			msg = e.Message
		}
		if msg != "" {
			if hint := statusHint(status); hint != "" {
				return fmt.Errorf("%s（HTTP %d）：%s", hint, status, msg)
			}
			return fmt.Errorf("%s（HTTP %d）", msg, status)
		}
	}
	if hint := statusHint(status); hint != "" {
		return fmt.Errorf("%s（HTTP %d）", hint, status)
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return fmt.Errorf("HTTP %d", status)
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return fmt.Errorf("HTTP %d：%s", status, s)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}
