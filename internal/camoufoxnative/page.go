package camoufoxnative

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/waa/prooftime"
	"github.com/Mag1cFall/AIStudio2API/internal/waa/rhythm"
)

// pageDOMHelpers 定义官网页面共用的可见目标与按钮状态判断
const pageDOMHelpers = `
  const visible = element => element.checkVisibility({visibilityProperty: true});
  const uniqueVisible = (selector, label) => {
    const items = [...document.querySelectorAll(selector)].filter(visible);
    if (items.length > 1) throw new Error(label + ' 匹配多个可见目标');
    return items[0];
  };
  const buttonEnabled = button => !button.matches(':disabled') && !button.closest('[aria-disabled="true"]');
`

// promptReadyExpression 检查登录与生成流程使用的同一个可见输入框
const promptReadyExpression = `(() => {` + pageDOMHelpers + `
  return Boolean(uniqueVisible('ms-prompt-box textarea', '提示词输入框'));
})()`

// workerPageReadyExpression 等待输入框出现或页面跳转到登录入口
const workerPageReadyExpression = `(location.hostname === 'accounts.google.com' || ` + promptReadyExpression + `)`

// loggingContextReadyExpression 检查官网页面已收到 GetLoggingContext 响应，此后页面 RPC 才带 x-goog-ext-519733851-bin
const loggingContextReadyExpression = `performance.getEntriesByType('resource').some(entry => entry.name.endsWith('/GetLoggingContext'))`

// normalizePromptNewlines 把 CRLF/CR 统一成 LF
func normalizePromptNewlines(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

// fillPromptStatements 向当前可见提示框写入参数 prompt 并通知页面表单，textarea 为该输入框，setValue 为 value 的原生 setter，synced 表示页面值与 prompt 相同
const fillPromptStatements = pageDOMHelpers + `
  const textarea = uniqueVisible('ms-prompt-box textarea', '提示词输入框');
  if (!textarea) throw new Error('提示词输入框不存在');
  const setValue = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
  setValue.call(textarea, prompt);
  textarea.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'insertText', data: prompt}));
  textarea.dispatchEvent(new Event('change', {bubbles: true}));
  const synced = textarea.value === prompt;
`

// proofTimingFunction 是在调用官网高层 proof 函数之前开始计时的函数声明：参数为官网 WAA service，service 自身与其 VM 管理对象上的初始化与到期重建 Promise 每完成一个记下一次时刻，先于官网函数对同一 Promise 的等待；返回的函数给出等待初始化与重建、生成 proof 的毫秒数
const proofTimingFunction = `service => {
  const started = performance.now();
  let refreshed = started;
  const done = () => { refreshed = performance.now(); };
  const vm = Object.values(service).find(value => typeof value?.snapshot === 'function');
  for (const wait of [...Object.values(service), ...Object.values(vm ?? {}).map(value => value?.promise)]) {
    if (typeof wait?.then === 'function') wait.then(done, done);
  }
  return () => ({refresh: refreshed - started, compute: performance.now() - refreshed});
}`

// pageProofTiming 是页面一次取 proof 的分项毫秒数
type pageProofTiming struct {
	// Refresh 是等待官网 WAA 初始化与 VM 到期重建的毫秒数
	Refresh float64 `json:"refresh"`
	// Compute 是 VM 生成 proof 的毫秒数
	Compute float64 `json:"compute"`
}

// observe 把分项累计进 ctx 所带的取 proof 耗时，自 started 起的其余时间记为界面操作
func (timing pageProofTiming) observe(ctx context.Context, started time.Time) {
	refresh := time.Duration(timing.Refresh * float64(time.Millisecond))
	compute := time.Duration(timing.Compute * float64(time.Millisecond))
	prooftime.Observe(ctx, prooftime.Refresh, refresh)
	prooftime.Observe(ctx, prooftime.Compute, compute)
	prooftime.Observe(ctx, prooftime.Interaction, time.Since(started)-refresh-compute)
}

// proofFunction 写入提示词后以保存的官方 WAA service 为 digest 调用官网 snapshot 函数，返回 proof 与 pageProofTiming 分项的 JSON，之后清空输入框并通知页面表单；页面值与提示词不同时不调用 snapshot，返回 null
const proofFunction = `async (page, prompt, digest) => {
  if (!page.service) throw new Error('官方 WAA service 尚未就绪');` + fillPromptStatements + `  try {
    if (!synced) return null;
    const timing = (` + proofTimingFunction + `)(page.service);
    const proof = await page.snapshot(digest);
    return JSON.stringify({proof, ...timing()});
  } finally {
    setValue.call(textarea, '');
    textarea.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'deleteContent'}));
  }
}`

// clipboardWriteFunction 把参数文本写入剪贴板，调用时带用户激活
const clipboardWriteFunction = `text => navigator.clipboard.writeText(text)`

// controlKey 是 WebDriver 修饰键 Control 的键码
const controlKey = "\uE009"

// shortcutKeyActions 生成带随机按键停顿的 Ctrl+<key> 可信键盘动作
func shortcutKeyActions(key string) []map[string]any {
	d1 := 18 + rand.IntN(25)
	d2 := 20 + rand.IntN(25)
	d3 := 55 + rand.IntN(45)
	d4 := 18 + rand.IntN(25)
	return []map[string]any{
		{"type": "keyDown", "value": controlKey}, {"type": "pause", "duration": d1},
		{"type": "keyDown", "value": key}, {"type": "pause", "duration": d2}, {"type": "pause", "duration": d3},
		{"type": "keyUp", "value": key}, {"type": "pause", "duration": d4},
		{"type": "keyUp", "value": controlKey},
	}
}

// 官网现场中 Firefox 在 BiDi 停顿之外为各组事件多用的毫秒数：Control 到 V、V 按住、V 抬起到 Control 抬起、指针移到按下与按下到抬起
const (
	controlOverhead = 4.5
	keyOverhead     = 9.5
	releaseOverhead = 3
	pressOverhead   = 8
	holdOverhead    = 8
)

// pause 返回让 Firefox 派发的两组事件相隔 interval 毫秒的 BiDi 停顿动作
func pause(interval, overhead float64) map[string]any {
	return map[string]any{"type": "pause", "duration": int(math.Round(max(interval-overhead, 0)))}
}

// pasteKeyActions 返回按账户节奏在官网输入框按 Ctrl+V 的可信键盘动作：按下 Control 与 V，按住后依次抬起；shift 为真时在 Control 之后按下 Shift、抬起 V 之后抬起 Shift，以 Ctrl+Shift+V 粘贴
func pasteKeyActions(account rhythm.Rhythm, shift bool) []map[string]any {
	if !shift {
		return []map[string]any{
			{"type": "keyDown", "value": "\uE009"}, pause(account.Control, controlOverhead),
			{"type": "keyDown", "value": "v"}, pause(account.Key, keyOverhead),
			{"type": "keyUp", "value": "v"}, pause(account.Release, releaseOverhead),
			{"type": "keyUp", "value": "\uE009"},
		}
	}
	return []map[string]any{
		{"type": "keyDown", "value": "\uE009"}, pause(account.Control, controlOverhead),
		{"type": "keyDown", "value": "\uE008"}, pause(account.Control, controlOverhead),
		{"type": "keyDown", "value": "v"}, pause(account.Key, keyOverhead),
		{"type": "keyUp", "value": "v"}, pause(account.Release, releaseOverhead),
		{"type": "keyUp", "value": "\uE008"}, pause(account.Release, releaseOverhead),
		{"type": "keyUp", "value": "\uE009"},
	}
}

// pasteLinkPattern 匹配官网提示词输入框按 Ctrl+V 粘贴时取出为附件的 Google 文档与云端硬盘、YouTube 与 Figma 链接（官网对 YouTube 链接不要求协议头）；按住 Shift 粘贴时页面不取出
var pasteLinkPattern = regexp.MustCompile(`(?i)(?:docs\.google\.com|drive\.google\.com|youtube(?:-nocookie)?\.com|youtu\.be|figma\.com)/`)

// promptValueFunction 返回可见提示框的当前值
const promptValueFunction = `() => {` + pageDOMHelpers + `
  const textarea = uniqueVisible('ms-prompt-box textarea', '提示词输入框');
  if (!textarea) throw new Error('提示词输入框不存在');
  return textarea.value;
}`

// pasteText 把 text 写入剪贴板，再以可信键盘按 Ctrl+V 粘贴到有焦点的输入框，shift 为真时按 Ctrl+Shift+V
func pasteText(ctx context.Context, client *bidiClient, contextID, text string, shift bool) error {
	if _, err := client.invoke(ctx, contextID, clipboardWriteFunction, []any{text}, map[string]any{"userActivation": true}); err != nil {
		return fmt.Errorf("写入剪贴板: %w", err)
	}
	return pressKeys(ctx, client, contextID, pasteKeyActions(client.rhythm, shift))
}

// pressKeys 以可信键盘依次执行 actions
func pressKeys(ctx context.Context, client *bidiClient, contextID string, actions []map[string]any) error {
	_, err := client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "key", "id": "keyboard", "actions": actions}},
	})
	return err
}

// pastePrompt 把按 LF 规范化的提示词写入剪贴板，再以可信键盘按 Ctrl+V 粘贴到有焦点的官网提示框，提示词含页面会取出为附件的链接时按 Ctrl+Shift+V，页面值与之不同时返回错误
func pastePrompt(ctx context.Context, client *bidiClient, contextID, prompt string) error {
	prompt = normalizePromptNewlines(prompt)
	if err := pasteText(ctx, client, contextID, prompt, pasteLinkPattern.MatchString(prompt)); err != nil {
		return fmt.Errorf("粘贴提示词: %w", err)
	}
	value, err := client.callFunctionString(ctx, contextID, promptValueFunction)
	if err != nil {
		return fmt.Errorf("读取提示词: %w", err)
	}
	if value != prompt {
		return fmt.Errorf("粘贴提示词后页面值不同 prompt=%q", prompt)
	}
	return nil
}

// runStateFunction 是等页面执行完已排队的任务后读取官网 Run 按钮状态的函数声明，页面忙于处理时随之等待；参数为 Worker 页面对象与界面运行的请求 ID，返回 runState 的 JSON
const runStateFunction = `async (page, id) => {` + pageDOMHelpers + `
  await new Promise(resolve => setTimeout(resolve));
  const describe = node => (node?.getAttribute('aria-describedby') || '').split(' ').map(name => document.getElementById(name)?.textContent.trim()).filter(Boolean).join(' ');
  const texts = selector => [...document.querySelectorAll(selector)].filter(visible).map(node => node.innerText.trim()).filter(Boolean);
  const button = uniqueVisible('ms-run-button button', '官网 Run 按钮');
  const box = document.querySelector('ms-prompt-box .prompt-box-container');
  let cover = '';
  if (button) {
    const rect = button.getBoundingClientRect();
    const top = document.elementFromPoint(rect.left + rect.width / 2, rect.top + rect.height / 2);
    if (top && !button.contains(top)) cover = (top.innerText || top.getAttribute('aria-label') || top.tagName).trim();
  }
  return JSON.stringify({
    found: Boolean(button), enabled: Boolean(button && buttonEnabled(button)) && !cover, running: Boolean(button?.querySelector('.spin')),
    started: Boolean(page.requests?.get(id)?.started),
    tooltip: describe(button), boxTooltip: box?.classList.contains('disabled') ? describe(box) : '',
    dialogs: texts('mat-dialog-container'), notices: texts('mat-snack-bar-container'), cover,
    prompt: document.querySelector('ms-prompt-box textarea')?.value.length ?? -1
  });
}`

// runState 是页面空闲时官网 Run 按钮与页面提示的状态
type runState struct {
	// Found 与 Enabled 表示 Run 按钮可见与可点，按钮中心被其他元素遮挡时不可点
	Found   bool `json:"found"`
	Enabled bool `json:"enabled"`
	// Running 表示 Run 按钮已切换为 Stop
	Running bool `json:"running"`
	// Started 表示页面应用已为本次请求调用高层 proof 函数
	Started bool `json:"started"`
	// Tooltip 与 BoxTooltip 是 Run 按钮与停用的输入框的悬停提示
	Tooltip    string   `json:"tooltip"`
	BoxTooltip string   `json:"boxTooltip"`
	Dialogs    []string `json:"dialogs"`
	Notices    []string `json:"notices"`
	// Cover 是遮挡 Run 按钮中心的元素文字
	Cover string `json:"cover"`
	// Prompt 是输入框文本的 UTF-16 长度，没有输入框时为 -1
	Prompt int `json:"prompt"`
}

// readRunState 读取页面空闲时官网 Run 按钮的状态，id 为界面运行的请求 ID
func readRunState(ctx context.Context, client *bidiClient, contextID string, page pageHandle, id string) (runState, error) {
	var state runState
	value, err := client.callFunctionString(ctx, contextID, runStateFunction, page, id)
	if err != nil {
		return state, fmt.Errorf("读取官网 Run 按钮状态: %w", err)
	}
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return state, fmt.Errorf("解析官网 Run 按钮状态: %w", err)
	}
	return state, nil
}

// reason 返回官网页面给出的 Run 按钮不可点或点击后没有运行的原因
func (state runState) reason() string {
	var parts []string
	if !state.Found {
		parts = append(parts, "Run 按钮不存在")
	}
	for _, dialog := range state.Dialogs {
		parts = append(parts, fmt.Sprintf("对话框 %q", dialog))
	}
	if state.Cover != "" {
		parts = append(parts, fmt.Sprintf("遮挡 Run 按钮的元素 %q", state.Cover))
	}
	if state.BoxTooltip != "" {
		parts = append(parts, fmt.Sprintf("输入框提示 %q", state.BoxTooltip))
	}
	if state.Tooltip != "" {
		parts = append(parts, fmt.Sprintf("Run 按钮提示 %q", state.Tooltip))
	}
	for _, notice := range state.Notices {
		parts = append(parts, fmt.Sprintf("提示条 %q", notice))
	}
	parts = append(parts, fmt.Sprintf("输入框 %d 字", state.Prompt))
	return strings.Join(parts, " | ")
}

// runSettleChecks 是页面空闲时 Run 按钮持续不可点、点击后仍没有开始运行时判定失败所需的连续检查次数，页面空闲时约 3 秒
const runSettleChecks = 40

// generationRunningExpression 检查官网 Run 按钮已切换为 Stop（按钮内出现 .spin）
const generationRunningExpression = `(() => {` + pageDOMHelpers + `
  const button = uniqueVisible('ms-run-button button', '官网 Run 按钮');
  return Boolean(button && button.querySelector('.spin'));
})()`

// promptFocusedExpression 检查输入框已获得焦点，官网新开对话后焦点在输入框中
const promptFocusedExpression = `Boolean(document.activeElement && document.activeElement.matches('` + promptBoxSelector + `'))`

// chatEmptyExpression 检查页面没有对话轮次
const chatEmptyExpression = `document.querySelectorAll('ms-chat-turn').length === 0`

// temporaryChatExpression 检查页面显示临时对话标识
const temporaryChatExpression = `[...document.querySelectorAll('ms-incognito-mode-indicator')].some(node => node.checkVisibility({visibilityProperty: true}))`

const (
	// promptBoxSelector 是提示词输入框
	promptBoxSelector = "ms-prompt-box textarea"
	// runButtonSelector 是生成前显示 Run、生成中显示 Stop 的按钮
	runButtonSelector = "ms-run-button button"
	// newChatButtonSelector 是顶栏的 New chat 按钮
	newChatButtonSelector = `ms-playground-toolbar button[aria-label="New chat"]`
	// discardButtonPattern 是 New chat 确认弹窗中丢弃当前对话的按钮文字
	discardButtonPattern = "^Discard and continue$"
	// lastTurnSelector 是最后一个对话轮次，指针悬停其上时显示 Rerun 按钮
	lastTurnSelector = "ms-chat-turn:last-of-type"
	// rerunButtonSelector 是最后一个对话轮次的 Rerun 按钮
	rerunButtonSelector = `ms-chat-turn:last-of-type button[aria-label^="Rerun" i]`
)

// pointerTargetFunction 把 selector 匹配且文字或 aria-label 匹配 pattern 的第一个可见元素滚动到视口内，返回其与视口可见交集安全区内的随机坐标 JSON，没有或不可见时返回空字符串
const pointerTargetFunction = `(selector, pattern) => {
  const expression = new RegExp(pattern, 'i');
  const item = [...document.querySelectorAll(selector)].find(node => node.checkVisibility({visibilityProperty: true}) && expression.test((node.innerText || node.getAttribute('aria-label') || '').trim()));
  if (!item) return '';
  item.scrollIntoView({block: 'nearest', inline: 'nearest'});
  const box = item.getBoundingClientRect();
  const vw = window.innerWidth, vh = window.innerHeight;
  const left = Math.max(0, box.left), top = Math.max(0, box.top);
  const right = Math.min(vw, box.right), bottom = Math.min(vh, box.bottom);
  if (right - left <= 2 || bottom - top <= 2) return '';
  const rx = 0.3 + Math.random() * 0.4;
  const ry = 0.3 + Math.random() * 0.4;
  const x = Math.min(vw - 2, Math.max(1, Math.round(left + (right - left) * rx)));
  const y = Math.min(vh - 2, Math.max(1, Math.round(top + (bottom - top) * ry)));
  return JSON.stringify({x, y});
}`

// pointerMoveDuration 是可信指针从别处移到目标元素所用的基准毫秒数
const pointerMoveDuration = 120

// humanPointerMoves 生成从 (fromX, fromY) 到 (toX, toY) 的多段二次贝塞尔弧线、Smoothstep 慢-快-慢加减速 pointerMove 动作序列
func humanPointerMoves(fromX, fromY, toX, toY, totalDuration int) []map[string]any {
	dx := float64(toX - fromX)
	dy := float64(toY - fromY)
	dist := math.Hypot(dx, dy)
	if totalDuration <= 0 || dist < 12 {
		return []map[string]any{{"type": "pointerMove", "x": toX, "y": toY, "duration": max(0, totalDuration)}}
	}
	steps := 5 + rand.IntN(3) // 5~7 段插值
	if totalDuration < steps*10 {
		steps = max(1, totalDuration/10)
	}
	// 法向随机弧度偏移（距离的 -12% ~ +12%，钳制在 [-42, 42] 像素内）
	nx, ny := -dy/dist, dx/dist
	spread := min(42.0, dist*0.12)
	offset := (rand.Float64()*2 - 1) * spread
	along := 0.35 + rand.Float64()*0.30
	ctrlX := float64(fromX) + dx*along + nx*offset
	ctrlY := float64(fromY) + dy*along + ny*offset

	// 空间 Smoothstep s=t^2*(3-2t) 已提供自然的慢-快-慢钟形速度分布；时间步长按均匀基准叠加轻量微扰（0.88~1.12），避免双重缓动产生中间段瞬移突刺
	weights := make([]float64, steps)
	var weightSum float64
	for index := 0; index < steps; index++ {
		w := 0.88 + rand.Float64()*0.24
		weights[index] = w
		weightSum += w
	}

	moves := make([]map[string]any, 0, steps)
	remaining := totalDuration
	for step := 1; step <= steps; step++ {
		t := float64(step) / float64(steps)
		s := t * t * (3 - 2*t)
		inv := 1 - s
		x := int(math.Round(inv*inv*float64(fromX) + 2*inv*s*ctrlX + s*s*float64(toX)))
		y := int(math.Round(inv*inv*float64(fromY) + 2*inv*s*ctrlY + s*s*float64(toY)))
		if step == steps {
			x, y = toX, toY
		}
		dur := int(math.Round(float64(totalDuration) * weights[step-1] / weightSum))
		if step == steps {
			dur = max(10, remaining)
		} else {
			dur = max(10, min(dur, remaining-10*(steps-step)))
			remaining -= dur
		}
		moves = append(moves, map[string]any{"type": "pointerMove", "x": x, "y": y, "duration": dur})
	}
	return moves
}

// pointer 以可信指针沿多段贝塞尔弧线移到 selector 与 pattern 匹配的可见元素内部随机坐标，click 为真时再按账户节奏与微扰停顿、按下并抬起，返回是否找到该元素
func (client *bidiClient) pointer(ctx context.Context, contextID, selector, pattern string, click bool, move int) (bool, error) {
	located, err := client.callFunctionString(ctx, contextID, pointerTargetFunction, selector, pattern)
	if err != nil || located == "" {
		return false, err
	}
	var target struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.Unmarshal([]byte(located), &target); err != nil {
		return false, err
	}
	actualMove := move
	if actualMove > 0 {
		actualMove = max(80, move-20+rand.IntN(95))
	}
	fromX, fromY, initialized := client.pointerPosition(contextID)
	var actions []map[string]any
	if !initialized {
		actions = append(actions, map[string]any{"type": "pointerMove", "x": fromX, "y": fromY, "duration": 0})
	}
	actions = append(actions, humanPointerMoves(fromX, fromY, target.X, target.Y, actualMove)...)
	if click {
		pressPause := pause(client.rhythm.Press, pressOverhead)
		holdPause := pause(client.rhythm.Hold, holdOverhead)
		if client.rhythm.Hold <= 0 {
			holdPause = map[string]any{"type": "pause", "duration": 45 + rand.IntN(45)}
		}
		actions = append(actions, pressPause, map[string]any{"type": "pointerDown", "button": 0}, holdPause, map[string]any{"type": "pointerUp", "button": 0})
	}
	_, err = client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "pointer", "id": "mouse", "parameters": map[string]any{"pointerType": "mouse"}, "actions": actions}},
	})
	if err == nil {
		client.setPointerPosition(contextID, target.X, target.Y)
	}
	return true, err
}

// simulateReading 在一轮回答结束后的空闲期，将鼠标从 Run 按钮沿弧线移回最新对话阅读区，并执行轻量滚轮浏览；shouldAbort 返回真时提前让路
func (client *bidiClient) simulateReading(ctx context.Context, contextID string, shouldAbort func() bool) error {
	located, err := client.callFunctionString(ctx, contextID, pointerTargetFunction, lastTurnSelector, "")
	if err != nil || located == "" {
		return err
	}
	var target struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.Unmarshal([]byte(located), &target); err != nil {
		return err
	}
	if shouldAbort != nil && shouldAbort() {
		return nil
	}
	fromX, fromY, initialized := client.pointerPosition(contextID)
	var actions []map[string]any
	if !initialized {
		actions = append(actions, map[string]any{"type": "pointerMove", "x": fromX, "y": fromY, "duration": 0})
	}
	moveDur := 160 + rand.IntN(140)
	actions = append(actions, humanPointerMoves(fromX, fromY, target.X, target.Y, moveDur)...)
	if _, err := client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "pointer", "id": "mouse", "parameters": map[string]any{"pointerType": "mouse"}, "actions": actions}},
	}); err != nil {
		return err
	}
	client.setPointerPosition(contextID, target.X, target.Y)
	if shouldAbort != nil && shouldAbort() {
		return nil
	}
	// 70% 概率在阅读区做一次向上轻滚浏览再滚回底部（回滚略带正向余量，消除上滚下滚精确为 0 的机械对称）
	if rand.IntN(10) < 7 {
		deltaUp := -(90 + rand.IntN(150))
		deltaDown := -deltaUp + 8 + rand.IntN(32)
		durUp := 110 + rand.IntN(90)
		pause := 200 + rand.IntN(260)
		durDown := 110 + rand.IntN(90)
		_, _ = client.command(ctx, "input.performActions", map[string]any{
			"context": contextID,
			"actions": []map[string]any{{
				"type": "wheel",
				"id":   "wheel",
				"actions": []map[string]any{
					{"type": "scroll", "x": target.X, "y": target.Y, "deltaX": 0, "deltaY": deltaUp, "duration": durUp},
					{"type": "pause", "duration": pause},
					{"type": "scroll", "x": target.X, "y": target.Y, "deltaX": 0, "deltaY": deltaDown, "duration": durDown},
				},
			}},
		})
	}
	return nil
}

// dismissOverlaysExpression 关闭官网已知且可交互的启动弹层：G1 欢迎对话框、引导浏览浮层与 Cookie 提示条，提示条有拒绝按钮时拒绝，只有确认按钮时确认
const dismissOverlaysExpression = `(() => {` + pageDOMHelpers + `
  const selectors = [
    'ms-g1-welcome-dialog button',
    'button.xap-tour-exit-button',
    '#glue-cookie-notification-bar-1 .glue-cookie-notification-bar__reject',
    '#glue-cookie-notification-bar-1:not(:has(.glue-cookie-notification-bar__reject)) .glue-cookie-notification-bar__accept'
  ];
  let clicked = 0;
  for (const selector of selectors) {
    const button = uniqueVisible(selector, '启动弹层按钮');
    if (button && buttonEnabled(button)) {
      button.click();
      clicked++;
    }
  }
  return clicked;
})()`

// dismissKnownOverlays 关闭官网已知启动弹层，返回点击的按钮数
func dismissKnownOverlays(ctx context.Context, client *bidiClient, contextID string) (int, error) {
	clicked, err := client.evaluate(ctx, contextID, dismissOverlaysExpression)
	if err != nil {
		return 0, fmt.Errorf("处理 AI Studio 启动覆盖层: %w", err)
	}
	count, _ := number(clicked["value"])
	return int(count), nil
}
