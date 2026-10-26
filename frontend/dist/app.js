"use strict";

const LANGS = [
	{ code: "zh-CN", label: "简体中文", en: "Simplified Chinese" },
	{ code: "zh-TW", label: "繁體中文", en: "Traditional Chinese" },
	{ code: "en", label: "English", en: "English" },
	{ code: "ja", label: "日本語", en: "Japanese" },
	{ code: "ko", label: "한국어", en: "Korean" },
	{ code: "fr", label: "Français", en: "French" },
	{ code: "de", label: "Deutsch", en: "German" },
	{ code: "es", label: "Español", en: "Spanish" },
	{ code: "ru", label: "Русский", en: "Russian" },
	{ code: "pt", label: "Português", en: "Portuguese" },
	{ code: "it", label: "Italiano", en: "Italian" },
	{ code: "ar", label: "العربية", en: "Arabic" },
	{ code: "th", label: "ไทย", en: "Thai" },
	{ code: "vi", label: "Tiếng Việt", en: "Vietnamese" },
];

const PRESET_META = {
	deepseek: {
		color: "#4D6BFE",
		keyURL: "https://platform.deepseek.com/api_keys",
		models: ["deepseek-flash", "deepseek-chat", "deepseek-reasoner"],
	},
	bailian: {
		color: "#615CED",
		keyURL: "https://bailian.console.aliyun.com/?apiKey=1#/api-key",
		models: ["qwen-mt-flash", "qwen-mt-lite", "qwen-mt-plus", "qwen-plus"],
	},
};
const CUSTOM_COLOR = "#64748B";

const THEME_ICONS = { system: "◐", light: "☀", dark: "🌙" };
const THEME_NAMES = { system: "跟随系统", light: "浅色", dark: "深色" };
const themeMQ = window.matchMedia("(prefers-color-scheme: dark)");

let cfg = null; // 后端配置
let editCfg = null; // 设置弹窗中的草稿
let currentSeq = -1; // 当前翻译轮次
let debounceTimer = null;

const $ = (sel) => document.querySelector(sel);
const esc = (s) =>
	String(s ?? "").replace(/[&<>"']/g, (c) =>
		({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

window.addEventListener("DOMContentLoaded", boot);

async function boot() {
	fillTargetLangs();
	bindEvents();
	bindTranslateEvents();
	cfg = await window.go.main.App.GetConfig();
	cfg.providers = cfg.providers || [];
	$("#targetLang").value = cfg.targetLang || "en";
	applyTheme();
	renderEmptyHint();
}

/* ── 主题：默认跟随系统，按钮循环切换 system → light → dark ── */

function applyTheme() {
	const mode = (cfg && cfg.theme) || "system";
	const dark = mode === "dark" || (mode === "system" && themeMQ.matches);
	document.documentElement.dataset.theme = dark ? "dark" : "light";
	const btn = $("#btnTheme");
	if (btn) {
		btn.textContent = THEME_ICONS[mode] || "◐";
		btn.title = `主题：${THEME_NAMES[mode] || THEME_NAMES.system}（点击切换）`;
	}
}

function cycleTheme() {
	const order = ["system", "light", "dark"];
	const mode = (cfg && cfg.theme) || "system";
	const next = order[(order.indexOf(mode) + 1) % order.length];
	if (cfg) cfg.theme = next;
	applyTheme();
	window.go.main.App.SetTheme(next);
}

function fillTargetLangs() {
	$("#targetLang").innerHTML = LANGS.map(
		(l) => `<option value="${l.code}">${esc(l.label)}</option>`).join("");
}

/* ── 事件绑定 ─────────────────────────── */

function bindEvents() {
	const source = $("#source");

	source.addEventListener("input", () => {
		updateMeta();
		clearTimeout(debounceTimer);
		debounceTimer = setTimeout(() => doTranslate(), 700);
	});
	source.addEventListener("keydown", (e) => {
		if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
			e.preventDefault();
			doTranslate();
		}
	});

	$("#btnTranslate").addEventListener("click", doTranslate);
	$("#btnClear").addEventListener("click", () => {
		source.value = "";
		updateMeta();
		renderEmptyHint();
		source.focus();
	});

	$("#targetLang").addEventListener("change", () => {
		window.go.main.App.SetTargetLang($("#targetLang").value);
		if (source.value.trim()) doTranslate();
	});

	$("#btnSettings").addEventListener("click", openSettings);
	$("#btnTheme").addEventListener("click", cycleTheme);
	themeMQ.addEventListener("change", applyTheme);
	$("#btnCloseSettings").addEventListener("click", closeSettings);
	$("#btnCancelSettings").addEventListener("click", closeSettings);
	$("#btnSaveSettings").addEventListener("click", saveSettings);
	$("#hotkeyOn").addEventListener("change", () => {
		$("#hotkeyBox").classList.toggle("off", !$("#hotkeyOn").checked);
	});
	$("#hotkeyBox").addEventListener("click", startHotkeyCapture);
	document.addEventListener("keydown", hotkeyCaptureKeydown, true);
	$("#btnAddCustom").addEventListener("click", () => {
		const el = addCustomRow({
			id: "custom-" + Date.now(), type: "custom",
			name: "", baseUrl: "", apiKey: "", model: "", enabled: true,
		});
		el.querySelector(".p-name").focus();
	});

	$("#settingsOverlay").addEventListener("mousedown", (e) => {
		if (e.target === e.currentTarget) closeSettings();
	});
	document.addEventListener("keydown", (e) => {
		if (e.key === "Escape" && !$("#settingsOverlay").hidden) closeSettings();
	});

	// 结果卡片上的复制按钮（事件委托）
	$("#results").addEventListener("click", async (e) => {
		const btn = e.target.closest(".copy-btn");
		if (!btn) return;
		const body = btn.closest(".card").querySelector(".card-body");
		const ok = await copyText(body.innerText);
		btn.textContent = ok ? "已复制 ✓" : "复制失败";
		btn.classList.toggle("copied", ok);
		setTimeout(() => {
			btn.textContent = "复制";
			btn.classList.remove("copied");
		}, 1200);
	});
}

function bindTranslateEvents() {
	window.runtime.EventsOn("translate:chunk", (d) => {
		if (d.seq !== currentSeq) return;
		const body = cardBody(d.id);
		if (!body) return;
		const cur = body.textContent;
		// qwen-mt-plus/turbo 不支持增量流式，后端返回的是累积全文而非增量
		if (body.dataset.cum === "1") {
			body.textContent = d.text.startsWith(cur) ? d.text : cur + d.text;
		} else {
			body.textContent = cur + d.text;
		}
	});
	window.runtime.EventsOn("translate:done", (d) => {
		if (d.seq !== currentSeq) return;
		setStatus(d.id, "done", "完成");
	});
	window.runtime.EventsOn("translate:error", (d) => {
		if (d.seq !== currentSeq) return;
		setStatus(d.id, "error", "失败");
		const body = cardBody(d.id);
		if (body) {
			body.textContent = d.message;
			body.classList.add("error");
		}
	});
	// 划词翻译：全局快捷键抓取到文本后由后端发来
	window.runtime.EventsOn("quick:translate", (d) => {
		closeSettings();
		const text = (d && d.text) || "";
		$("#source").value = text;
		updateMeta();
		if (text) doTranslate();
		else renderEmptyHint("未能取到选中的文本：该程序可能不支持 Ctrl+C 复制划选内容");
	});
}

/* ── 翻译流程 ─────────────────────────── */

function doTranslate() {
	const text = $("#source").value.trim();
	if (!text) return;
	if (text.length > 20000) {
		renderEmptyHint("内容过长（超过 20000 字符），请分段翻译");
		return;
	}
	if (!cfg.providers.some((p) => p.enabled && p.apiKey && p.model)) {
		renderEmptyHint("尚未启用任何 AI 服务，点击右上角 ⚙ 进行配置");
		return;
	}
	window.go.main.App.TranslateAll(text, langEn($("#targetLang").value))
		.then((start) => {
			if (!start.providers || !start.providers.length) {
				renderEmptyHint("没有已启用的 AI 服务，点击右上角 ⚙ 配置");
				return;
			}
			currentSeq = start.seq;
			renderCards(start.providers);
		});
}

function renderCards(list) {
	const pane = $("#results");
	pane.innerHTML = "";
	for (const s of list) {
		const color = PRESET_META[s.id]?.color || CUSTOM_COLOR;
		const card = document.createElement("div");
		card.className = "card";
		card.dataset.id = s.id;
		// 非增量流式的模型标记为累积模式，收到全文时整体替换而不是追加
		if (/qwen-mt-(plus|turbo)/.test(s.model || "")) card.dataset.cum = "1";
		card.innerHTML = `
			<div class="card-head">
				<span class="dot" style="--c:${color}"></span>
				<span class="card-name">${esc(s.name)}</span>
				<span class="card-model" title="${esc(s.model)}">${esc(s.model)}</span>
				<span class="card-status" data-status="loading">翻译中</span>
				<button class="copy-btn">复制</button>
			</div>
			<div class="card-body"></div>`;
		pane.appendChild(card);
	}
}

function renderEmptyHint(msg) {
	const enabled = cfg ? cfg.providers.filter((p) => p.enabled).map((p) => p.name) : [];
	const status = enabled.length
		? `已启用：${enabled.join("、")}`
		: "尚未启用任何 AI 服务，点击右上角 ⚙ 配置";
	$("#results").innerHTML = `
		<div class="empty-hint">
			<div class="empty-icon">⇄</div>
			<div>${esc(msg || "输入内容，自动翻译到所选语言")}</div>
			<div class="empty-sub">${esc(status)}</div>
		</div>`;
}

function cardBody(id) {
	return document.querySelector(`.card[data-id="${CSS.escape(id)}"] .card-body`);
}

function setStatus(id, status, label) {
	const el = document.querySelector(`.card[data-id="${CSS.escape(id)}"] .card-status`);
	if (el) {
		el.dataset.status = status;
		el.textContent = label;
	}
}

async function copyText(s) {
	try {
		await navigator.clipboard.writeText(s);
		return true;
	} catch {
		try {
			const ta = document.createElement("textarea");
			ta.value = s;
			document.body.appendChild(ta);
			ta.select();
			document.execCommand("copy");
			ta.remove();
			return true;
		} catch {
			return false;
		}
	}
}

function updateMeta() {
	const text = $("#source").value;
	$("#charCount").textContent = text.length + " 字符";
	const lang = detectLang(text);
	const badge = $("#detected");
	if (lang && text.length > 0) {
		badge.hidden = false;
		badge.textContent = "自动检测：" + lang;
	} else {
		badge.hidden = true;
	}
}

/* ── 语言检测（Unicode 区间） ────────────── */

function detectLang(text) {
	if (!text) return null;
	const c = { han: 0, kana: 0, hangul: 0, cyrillic: 0, arabic: 0, thai: 0, latin: 0 };
	for (const ch of text) {
		const n = ch.codePointAt(0);
		if ((n >= 0x4e00 && n <= 0x9fff) || (n >= 0x3400 && n <= 0x4dbf) || (n >= 0xf900 && n <= 0xfaff)) c.han++;
		else if ((n >= 0x3040 && n <= 0x309f) || (n >= 0x30a0 && n <= 0x30ff)) c.kana++;
		else if (n >= 0xac00 && n <= 0xd7af) c.hangul++;
		else if (n >= 0x0400 && n <= 0x04ff) c.cyrillic++;
		else if (n >= 0x0600 && n <= 0x06ff) c.arabic++;
		else if (n >= 0x0e00 && n <= 0x0e7f) c.thai++;
		else if ((n >= 0x41 && n <= 0x5a) || (n >= 0x61 && n <= 0x7a) || (n >= 0xc0 && n <= 0x24f)) c.latin++;
	}
	if (c.kana) return "日语";
	if (c.hangul) return "韩语";
	if (c.han) return "中文";
	if (c.cyrillic) return "俄语";
	if (c.arabic) return "阿拉伯语";
	if (c.thai) return "泰语";
	if (c.latin) return "英语";
	return null;
}

function langEn(code) {
	const l = LANGS.find((x) => x.code === code);
	return l ? l.en : "English";
}

/* ── 设置弹窗 ─────────────────────────── */

function openSettings() {
	editCfg = JSON.parse(JSON.stringify(cfg));
	renderSettings();
	$("#hotkeyOn").checked = !editCfg.hotkeyOff;
	$("#hotkeyBox").textContent = editCfg.hotkey || "Ctrl+Shift+Space";
	$("#hotkeyBox").classList.toggle("off", !!editCfg.hotkeyOff);
	$("#doubleCopyOn").checked = !editCfg.doubleCopyOff;
	$("#settingsMsg").textContent = "";
	$("#settingsOverlay").hidden = false;
}

function closeSettings() {
	$("#settingsOverlay").hidden = true;
	editCfg = null;
}

function renderSettings() {
	$("#presetList").innerHTML = editCfg.providers
		.filter((p) => p.type === "preset").map(presetRowHTML).join("");
	const customs = editCfg.providers.filter((p) => p.type === "custom");
	$("#customList").innerHTML = customs.length
		? customs.map(customRowHTML).join("")
		: `<div class="custom-empty">还没有自定义服务</div>`;
	document.querySelectorAll("#settingsBody .provider").forEach(bindProviderRow);
}

function presetRowHTML(p) {
	const meta = PRESET_META[p.id] || {};
	const models = (meta.models || [])
		.map((m) => `<option value="${esc(m)}">`).join("");
	return `
	<div class="provider ${p.enabled ? "on" : ""}" data-id="${esc(p.id)}" data-ptype="preset">
		<div class="provider-head">
			<span class="dot" style="--c:${meta.color || CUSTOM_COLOR}"></span>
			<span class="provider-title">${esc(p.name)}</span>
			${meta.keyURL ? `<a class="key-link" href="${esc(meta.keyURL)}" data-url="${esc(meta.keyURL)}">获取 API Key ↗</a>` : ""}
			<label class="switch" title="启用">
				<input type="checkbox" class="p-enabled" ${p.enabled ? "checked" : ""}>
				<span class="slider"></span>
			</label>
		</div>
		<div class="provider-fields">
			<div class="field">
				<label>API Key</label>
				<input type="password" class="p-key" placeholder="粘贴 API Key" value="${esc(p.apiKey)}" autocomplete="off">
			</div>
			<div class="field">
				<label>模型</label>
				<input type="text" class="p-model" list="dl-${esc(p.id)}" placeholder="${esc(meta.modelPlaceholder || "模型名")}" value="${esc(p.model)}">
				<datalist id="dl-${esc(p.id)}">${models}</datalist>
			</div>
			<div class="field" style="grid-column: 1 / -1;">
				<label>服务地址 Base URL</label>
				<input type="text" class="p-url" placeholder="${esc(p.baseUrl || "")}" value="${esc(p.baseUrl)}">
			</div>
		</div>
	</div>`;
}

function customRowHTML(p) {
	return `
	<div class="provider ${p.enabled ? "on" : ""}" data-id="${esc(p.id)}" data-ptype="custom">
		<div class="provider-head">
			<span class="dot" style="--c:${CUSTOM_COLOR}"></span>
			<input type="text" class="p-name" placeholder="名称，如：其他平台" value="${esc(p.name)}">
			<label class="switch" title="启用">
				<input type="checkbox" class="p-enabled" ${p.enabled ? "checked" : ""}>
				<span class="slider"></span>
			</label>
			<button class="p-delete" title="删除">🗑</button>
		</div>
		<div class="provider-fields">
			<div class="field">
				<label>服务地址 Base URL</label>
				<input type="text" class="p-url" placeholder="https://…/v1" value="${esc(p.baseUrl)}">
			</div>
			<div class="field">
				<label>模型</label>
				<input type="text" class="p-model" placeholder="模型名" value="${esc(p.model)}">
			</div>
			<div class="field" style="grid-column: 1 / -1;">
				<label>API Key</label>
				<input type="password" class="p-key" placeholder="粘贴 API Key" value="${esc(p.apiKey)}" autocomplete="off">
			</div>
		</div>
	</div>`;
}

function addCustomRow(p) {
	$("#customList").insertAdjacentHTML("beforeend", customRowHTML(p));
	const el = document.querySelector(`#customList .provider[data-id="${CSS.escape(p.id)}"]`);
	bindProviderRow(el);
	return el;
}

function bindProviderRow(el) {
	const checkbox = el.querySelector(".p-enabled");
	checkbox.addEventListener("change", () => el.classList.toggle("on", checkbox.checked));

	el.querySelectorAll(".key-link").forEach((a) =>
		a.addEventListener("click", (e) => {
			e.preventDefault();
			window.runtime.BrowserOpenURL(a.dataset.url);
		}));

	const del = el.querySelector(".p-delete");
	if (del) del.addEventListener("click", () => el.remove());
}

function collectSettings() {
	const providers = [];
	document.querySelectorAll("#settingsBody .provider").forEach((el) => {
		const orig = editCfg.providers.find((p) => p.id === el.dataset.id) || {};
		const q = (s) => el.querySelector(s);
		providers.push({
			id: el.dataset.id,
			type: el.dataset.ptype || "custom",
			name: (q(".p-name")?.value ?? orig.name ?? "").trim() || "自定义服务",
			baseUrl: (q(".p-url")?.value ?? orig.baseUrl ?? "").trim(),
			apiKey: q(".p-key").value.trim(),
			model: q(".p-model").value.trim(),
			enabled: q(".p-enabled").checked,
		});
	});
	return {
		providers,
		targetLang: cfg.targetLang || "en",
		hotkey: editCfg.hotkey || "Ctrl+Shift+Space",
		hotkeyOff: !$("#hotkeyOn").checked,
		doubleCopyOff: !$("#doubleCopyOn").checked,
	};
}

async function saveSettings() {
	const payload = collectSettings();
	const bad = payload.providers.find(
		(p) => p.enabled && (!p.apiKey || !p.baseUrl || !p.model));
	if (bad) {
		$("#settingsMsg").textContent =
			`「${bad.name}」已启用但信息不完整（需要 API Key、服务地址和模型）`;
		return;
	}
	cfg = await window.go.main.App.SaveConfig(payload);
	closeSettings();
	if ($("#source").value.trim()) doTranslate();
	else renderEmptyHint();
}

/* ── 划词快捷键录制 ────────────────────── */

let capturingHotkey = false;

function startHotkeyCapture() {
	if (capturingHotkey) return;
	capturingHotkey = true;
	const el = $("#hotkeyBox");
	el.classList.add("capturing");
	el.textContent = "按下组合键…（Esc 取消）";
}

function hotkeyCaptureKeydown(e) {
	if (!capturingHotkey) return;
	// 独占按键：不触发页面快捷键（如 Esc 关设置）、不输入字符
	e.preventDefault();
	e.stopImmediatePropagation();
	if (e.key === "Escape") {
		endHotkeyCapture();
		return;
	}
	const combo = comboFromEvent(e);
	if (!combo) return; // 只按了修饰键，等待主键
	editCfg.hotkey = combo;
	endHotkeyCapture();
	$("#hotkeyBox").textContent = combo;
}

function endHotkeyCapture() {
	capturingHotkey = false;
	const el = $("#hotkeyBox");
	el.classList.remove("capturing");
	el.textContent = editCfg.hotkey || "Ctrl+Shift+Space";
}

// comboFromEvent 把按键事件转成 "Ctrl+Shift+T" 形式的组合键字符串，
// 主键用 e.code 识别以兼容非英文键盘布局；无修饰键或无主键时返回 null。
function comboFromEvent(e) {
	const names = {
		" ": "Space", Enter: "Enter", Tab: "Tab", Backspace: "Backspace",
		ArrowUp: "Up", ArrowDown: "Down", ArrowLeft: "Left", ArrowRight: "Right",
		Home: "Home", End: "End", PageUp: "PgUp", PageDown: "PgDn",
		Insert: "Insert", Delete: "Delete",
	};
	let key = null;
	if (/^Key[A-Z]$/.test(e.code)) key = e.code[3];
	else if (/^Digit[0-9]$/.test(e.code)) key = e.code[5];
	else if (/^F([1-9]|1[0-9]|2[0-4])$/.test(e.code)) key = e.code;
	else key = names[e.key] || null;
	if (!key) return null;
	const mods = [
		e.ctrlKey && "Ctrl", e.altKey && "Alt",
		e.shiftKey && "Shift", e.metaKey && "Win",
	].filter(Boolean);
	if (!mods.length) return null; // 必须带修饰键，避免劫持普通输入
	return [...mods, key].join("+");
}
