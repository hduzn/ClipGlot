# ClipGlot

轻巧的 Windows 桌面 AI 翻译工具。左侧输入原文（自动检测语言），右侧多个 AI 同时翻译，一键复制对比结果。基于 [Wails v2](https://wails.io)（Go + WebView2），无 Node 依赖，release 单文件约 13MB（已内嵌 WebView2 引导器，极少数没有 WebView2 运行时的 Win10 机器上首次运行会自动安装）。

直接双击 `release\ClipGlot_v1.0.0.exe` 即可使用。

## 功能

- **左侧输入 / 右侧结果**：停止输入约 0.7 秒后自动翻译，或按 `Ctrl+Enter` 立即翻译
- **语言自动检测**：基于 Unicode 区间识别中/日/韩/俄/阿拉伯/泰/拉丁语系，源语言无需选择
- **目标语言下拉选择**：简中、繁中、英、日、韩、法、德、西、俄、葡、意、阿拉伯、泰、越南
- **一键复制**：每个结果卡片右上角有复制按钮
- **多 AI 并行翻译**：已启用的服务同时请求，结果分卡片流式显示，方便对比
- **支持的服务**（均为 OpenAI 兼容接口，服务地址可在设置中修改）：
  - **阿里云百炼**，默认模型 `qwen-mt-flash`（翻译专用模型，自动按官方要求传
    `translation_options`、不使用 System Message，`source_lang=auto` 自动检测源语言）
  - **DeepSeek**，默认模型 `deepseek-flash`
  - 自定义 OpenAI 兼容供应商（可添加多个）
- 推理类模型的思考内容会被自动忽略，只显示译文；不支持增量流式的模型（如 `qwen-mt-plus`）自动按累积输出处理
- 单实例运行：重复启动会唤起已有窗口
- 主题默认跟随系统，顶栏 ◐ 按钮可循环切换 跟随系统 / 浅色 / 深色
- 错误提示本地化：余额不足、Key 无效、限流等都会给出中文提示（兼容各平台的错误格式）

## 使用

1. 双击 `release\` 目录下的 exe（首次运行无任何服务启用）
2. 点右上角 **⚙ 设置**，为要用的服务粘贴 API Key、确认模型并打开开关
3. 左侧输入内容即可翻译；每个结果卡片有独立的 **复制** 按钮

API Key 申请入口（按设置中的排列顺序）：

| 服务 | 地址 | 默认模型 |
|---|---|---|
| 阿里云百炼 | bailian.console.aliyun.com（API-Key 管理） | `qwen-mt-flash` |
| DeepSeek | platform.deepseek.com/api_keys | `deepseek-flash` |

配置保存在 **exe 同目录的 `config.json`**（便携模式；exe 所在目录不可写时自动改用
`%AppData%\ClipGlot\config.json`，旧版 %AppData% 配置会在启动时自动迁移）。
文件含 API Key 明文，请勿外传。

> 升级说明：若之前保存过设置，旧配置优先于新默认值。要让百炼改用 `qwen-mt-flash`，
> 在设置里把模型改过来即可；删除 `%AppData%\ClipGlot\config.json` 可完全重置。

## 开发 & 构建

要求：Go 1.21+、[Wails CLI](https://wails.io/docs/gettingstarted/installation)、WebView2 Runtime（Win11 自带，Win10 一般已有）。

```bash
wails dev      # 开发热重载
build.bat      # 一键构建发布版
```

构建流程：

- `wails build` 产物固定输出到 **`build\bin\ClipGlot.exe`**
- `build.bat` 在此基础上额外复制一份带版本号的发布版到 **`release\ClipGlot_v1.0.0.exe`**
- 版本号唯一来源是 [wails.json](wails.json) 的 `info.productVersion`，改版本只需改这一处，文件名与 exe 属性会同步变化

> ⚠️ 不要用裸 `go build` 出发布版：缺少 `production` 构建标签时，程序启动只会弹一个
> "Wails applications will not build without the correct build tags" 的错误框。

## 项目结构

```
main.go        程序入口、窗口与单实例配置
app.go         绑定到前端的方法：配置读写、并发翻译调度、事件推送
config.go      配置定义与 %AppData% 读写
llm.go         OpenAI 兼容 /chat/completions 流式客户端（SSE）；qwen-mt 翻译专用协议
frontend/dist  界面（原生 HTML/CSS/JS，无框架）
build/         构建资源（应用图标 appicon.png、Windows 清单等）；build/bin 为编译产物
release/       发布版（ClipGlot_v版本号.exe，由 build.bat 生成）
```
