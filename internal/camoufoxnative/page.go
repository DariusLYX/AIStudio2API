package camoufoxnative

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
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

// proofFunction 写入提示词后以保存的官方 WAA service 为 digest 调用官网 snapshot 函数，之后清空输入框并通知页面表单；页面值与提示词不同时不调用 snapshot，返回 null
const proofFunction = `async (page, prompt, digest) => {
  if (!page.service) throw new Error('官方 WAA service 尚未就绪');` + fillPromptStatements + `  try {
    return synced ? await page.snapshot(digest) : null;
  } finally {
    setValue.call(textarea, '');
    textarea.dispatchEvent(new InputEvent('input', {bubbles: true, inputType: 'deleteContent'}));
  }
}`

// clipboardWriteFunction 把参数文本写入剪贴板，调用时带用户激活
const clipboardWriteFunction = `text => navigator.clipboard.writeText(text)`

// shortcutKeyActions 生成带随机按键停顿的 Ctrl+<key> 可信键盘动作
func shortcutKeyActions(key string) []map[string]any {
	control := pasteKeyActions[0]["value"]
	d1 := 18 + rand.IntN(25)
	d2 := 20 + rand.IntN(25)
	d3 := 55 + rand.IntN(45)
	d4 := 18 + rand.IntN(25)
	return []map[string]any{
		{"type": "keyDown", "value": control}, {"type": "pause", "duration": d1},
		{"type": "keyDown", "value": key}, {"type": "pause", "duration": d2}, {"type": "pause", "duration": d3},
		{"type": "keyUp", "value": key}, {"type": "pause", "duration": d4},
		{"type": "keyUp", "value": control},
	}
}

// pasteKeyActions 是在官网输入框按 Ctrl+V 的可信键盘动作：按下 Control 与 V，按住 70 ms 后依次抬起
var pasteKeyActions = []map[string]any{
	{"type": "keyDown", "value": "\uE009"}, {"type": "pause", "duration": 25},
	{"type": "keyDown", "value": "v"}, {"type": "pause", "duration": 25}, {"type": "pause", "duration": 70},
	{"type": "keyUp", "value": "v"}, {"type": "pause", "duration": 25},
	{"type": "keyUp", "value": "\uE009"},
}

// promptValueFunction 返回可见提示框的当前值
const promptValueFunction = `() => {` + pageDOMHelpers + `
  const textarea = uniqueVisible('ms-prompt-box textarea', '提示词输入框');
  if (!textarea) throw new Error('提示词输入框不存在');
  return textarea.value;
}`

// pasteText 把 text 写入剪贴板，再以可信键盘按 Ctrl+V 粘贴到有焦点的输入框
func pasteText(ctx context.Context, client *bidiClient, contextID, text string) error {
	if _, err := client.invoke(ctx, contextID, clipboardWriteFunction, []any{text}, map[string]any{"userActivation": true}); err != nil {
		return fmt.Errorf("写入剪贴板: %w", err)
	}
	return pressKeys(ctx, client, contextID, shortcutKeyActions("v"))
}

// pressKeys 以可信键盘依次执行 actions
func pressKeys(ctx context.Context, client *bidiClient, contextID string, actions []map[string]any) error {
	_, err := client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "key", "id": "keyboard", "actions": actions}},
	})
	return err
}

// pastePrompt 把按 LF 规范化的提示词写入剪贴板，再以可信键盘按 Ctrl+V 粘贴到有焦点的官网提示框，页面值与之不同时返回错误
func pastePrompt(ctx context.Context, client *bidiClient, contextID, prompt string) error {
	prompt = normalizePromptNewlines(prompt)
	if err := pasteText(ctx, client, contextID, prompt); err != nil {
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

// pointerTargetFunction 把 selector 匹配且文字或 aria-label 匹配 pattern 的第一个可见元素滚动到视口内，返回其内部安全区随机坐标 JSON，没有时返回空字符串
const pointerTargetFunction = `(selector, pattern) => {
  const expression = new RegExp(pattern, 'i');
  const item = [...document.querySelectorAll(selector)].find(node => node.checkVisibility({visibilityProperty: true}) && expression.test((node.innerText || node.getAttribute('aria-label') || '').trim()));
  if (!item) return '';
  item.scrollIntoView({block: 'nearest', inline: 'nearest'});
  const box = item.getBoundingClientRect();
  const rx = 0.3 + Math.random() * 0.4;
  const ry = 0.3 + Math.random() * 0.4;
  return JSON.stringify({x: Math.round(box.left + box.width * rx), y: Math.round(box.top + box.height * ry)});
}`

// pointerMoveDuration 是可信指针从别处移到目标元素所用的基准毫秒数
const pointerMoveDuration = 120

// humanPointerMoves 生成从 (fromX, fromY) 到 (toX, toY) 的多段二次贝塞尔微弧度、慢-快-慢加减速 pointerMove 动作序列
func humanPointerMoves(fromX, fromY, toX, toY, totalDuration int) []map[string]any {
	dx := float64(toX - fromX)
	dy := float64(toY - fromY)
	dist := math.Hypot(dx, dy)
	if totalDuration <= 0 || dist < 12 {
		return []map[string]any{{"type": "pointerMove", "x": toX, "y": toY, "duration": max(0, totalDuration)}}
	}
	steps := 5 + rand.IntN(3) // 5~7 段插值
	// 法向随机弧度偏移（距离的 -12% ~ +12%，钳制在 [-42, 42] 像素内）
	nx, ny := -dy/dist, dx/dist
	spread := min(42.0, dist*0.12)
	offset := (rand.Float64()*2 - 1) * spread
	along := 0.35 + rand.Float64()*0.30
	ctrlX := float64(fromX) + dx*along + nx*offset
	ctrlY := float64(fromY) + dy*along + ny*offset

	// 两端慢、中间快的时间权重（模拟人类启动加速与接近目标减速对准）
	weights := make([]float64, steps)
	var weightSum float64
	for index := 0; index < steps; index++ {
		mid := float64(steps-1) / 2.0
		edge := math.Abs(float64(index)-mid) / max(1.0, mid)
		w := 0.75 + 0.65*edge
		weights[index] = w
		weightSum += w
	}

	moves := make([]map[string]any, 0, steps)
	remaining := totalDuration
	for step := 1; step <= steps; step++ {
		t := float64(step) / float64(steps)
		// Smoothstep 缓入缓出空间采样
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

// pointer 以可信指针沿多段贝塞尔弧线移到 selector 与 pattern 匹配的可见元素内部随机坐标，click 为真时再按下、随机停顿并抬起，返回是否找到该元素
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
	fromX, fromY := client.pointerPosition()
	actions := humanPointerMoves(fromX, fromY, target.X, target.Y, actualMove)
	if click {
		hold := 45 + rand.IntN(45)
		actions = append(actions, map[string]any{"type": "pointerDown", "button": 0}, map[string]any{"type": "pause", "duration": hold}, map[string]any{"type": "pointerUp", "button": 0})
	}
	_, err = client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "pointer", "id": "mouse", "parameters": map[string]any{"pointerType": "mouse"}, "actions": actions}},
	})
	if err == nil {
		client.setPointerPosition(target.X, target.Y)
	}
	return true, err
}

// simulateReading 在一轮回答结束后的空闲期，将鼠标从 Run 按钮沿弧线移回最新对话阅读区，并执行轻量滚轮浏览
func (client *bidiClient) simulateReading(ctx context.Context, contextID string) error {
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
	fromX, fromY := client.pointerPosition()
	moveDur := 160 + rand.IntN(140)
	moves := humanPointerMoves(fromX, fromY, target.X, target.Y, moveDur)
	if _, err := client.command(ctx, "input.performActions", map[string]any{
		"context": contextID,
		"actions": []map[string]any{{"type": "pointer", "id": "mouse", "parameters": map[string]any{"pointerType": "mouse"}, "actions": moves}},
	}); err != nil {
		return err
	}
	client.setPointerPosition(target.X, target.Y)
	// 70% 概率在阅读区做一次向上轻滚浏览再滚回底部
	if rand.IntN(10) < 7 {
		deltaUp := -(90 + rand.IntN(150))
		deltaDown := -deltaUp
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
