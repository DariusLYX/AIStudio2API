package camoufoxnative

import (
	"encoding/json"
	"math"
	"testing"
)

// TestNormalizePromptNewlines 覆盖官网 textarea 把写入值规范化为 LF 的比较场景
func TestNormalizePromptNewlines(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{name: "lf", value: "a\nb", want: "a\nb"},
		{name: "crlf", value: "a\r\nb", want: "a\nb"},
		{name: "cr", value: "a\rb", want: "a\nb"},
		{name: "mixed", value: "a\r\nb\nc\rd", want: "a\nb\nc\nd"},
		{name: "empty", value: "", want: ""},
		{name: "no newline", value: "abc", want: "abc"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := normalizePromptNewlines(item.value); got != item.want {
				t.Fatalf("normalizePromptNewlines(%q) = %q, want %q", item.value, got, item.want)
			}
		})
	}
}

// TestNormalizePromptNewlinesMatchesTextareaValue 确认 CRLF 提示词与官网返回的 LF 值判定为同步
func TestNormalizePromptNewlinesMatchesTextareaValue(t *testing.T) {
	prompt := "line1\r\nline2\r\nline3"
	textarea := "line1\nline2\nline3"
	if prompt == textarea {
		t.Fatal("用例前提失效: CRLF 提示词不应与 textarea 值直接相等")
	}
	if normalizePromptNewlines(prompt) != normalizePromptNewlines(textarea) {
		t.Fatal("CRLF 提示词与 textarea LF 值应判定为已同步")
	}
}

// TestNextChatActionMultiTurn 验证多轮对话在去掉自动清空后能够沿用同一页面继续对话，包括真实 0-based 下标的流式分块思考模型轮与非文本 Part 区分
func TestNextChatActionMultiTurn(t *testing.T) {
	turn1User := json.RawMessage(`[[[null,"第一轮问题"]],"user"]`)
	// 服务端流式分块还原的模型轮：part[12]=1 为思考 Part，part[14] 为思考签名，两个文本分块 "第一轮"、"回答"
	turn1ModelStreamed := json.RawMessage(`[[[null,"思考过程",null,null,null,null,null,null,null,null,null,null,1],[null,"第一轮"],[null,"回答",null,null,null,null,null,null,null,null,null,null,null,null,"sig_abc"]],"model"]`)
	// 客户端回传的合并后单段模型轮（不含思考 Part 与签名）
	turn1ModelEchoed := json.RawMessage(`[[[null,"第一轮回答"]],"model"]`)
	turn2User := json.RawMessage(`[[[null,"第二轮问题"]],"user"]`)
	turn2Model := json.RawMessage(`[[[null,"第二轮回答"]],"model"]`)
	turn3User := json.RawMessage(`[[[null,"第三轮问题"]],"user"]`)

	// 第 1 轮 -> 第 2 轮
	prev1 := []json.RawMessage{turn1User}
	curr2 := []json.RawMessage{turn1User, turn1ModelStreamed, turn2User}
	if got := nextChatAction(prev1, curr2); got != chatContinue {
		t.Fatalf("第 2 轮期望 chatContinue，实际为 %v", got)
	}

	// 第 2 轮 -> 第 3 轮（客户端回传的第 1 轮模型回复为合并后的单段文本且无思考 Part/签名）
	curr3 := []json.RawMessage{turn1User, turn1ModelEchoed, turn2User, turn2Model, turn3User}
	if got := nextChatAction(curr2, curr3); got != chatContinue {
		t.Fatalf("第 3 轮（分块带签名模型回复对比合并回复）期望 chatContinue，实际为 %v", got)
	}

	// 同文字但不同图片/文件附件的轮次不应被判定为相同（防止误判为 Rerun）
	userWithImgA := json.RawMessage(`[[[null,"看图"],[null,null,["image/png","AAAA"]]],"user"]`)
	userWithImgB := json.RawMessage(`[[[null,"看图"],[null,null,["image/png","BBBB"]]],"user"]`)
	if got := nextChatAction([]json.RawMessage{userWithImgA}, []json.RawMessage{userWithImgB}); got != chatNew {
		t.Fatalf("同文字换图期望 chatNew，实际为 %v", got)
	}

	// 两个只有函数调用（part[10]）但函数名/参数不同、且携带相同占位签名（part[14]）的模型轮不应被判为相同
	fnCallA := json.RawMessage(`[[[null,null,null,null,null,null,null,null,null,null,["read_file",{"path":"a.txt"}],null,null,null,"skip_thought_signature_validator"]],"model"]`)
	fnCallB := json.RawMessage(`[[[null,null,null,null,null,null,null,null,null,null,["read_file",{"path":"b.txt"}],null,null,null,"skip_thought_signature_validator"]],"model"]`)
	if sameTurnContent(fnCallA, fnCallB) {
		t.Fatal("不同函数调用（part[10]）不应被判定为相同轮次")
	}
	fnCallASameNoSig := json.RawMessage(`[[[null,null,null,null,null,null,null,null,null,null,["read_file",{"path":"a.txt"}]]],"model"]`)
	if !sameTurnContent(fnCallA, fnCallASameNoSig) {
		t.Fatal("相同函数调用仅思考签名（part[14]）不同时应判定为相同轮次")
	}

	// 开启全新对话
	newChat := []json.RawMessage{json.RawMessage(`[[[null,"全新话题"]],"user"]`)}
	if got := nextChatAction(curr2, newChat); got != chatNew {
		t.Fatalf("新话题期望 chatNew，实际为 %v", got)
	}
}

// TestGeneratePrompt 验证最后一轮提示词提取以及函数结果轮不误判为文本输入
func TestGeneratePrompt(t *testing.T) {
	body := []byte(`["models/gemini-4-carbon",[[[[null,"看图说话"],[null,null,["image/png","iVBORw0KGgo="]]],"user"]],null,[null,null,null,65536],"!PROOF",[[[null,"长记忆系统指令"]],"user"]]`)
	prompt, ok := GeneratePrompt(body)
	if !ok || prompt != "看图说话" {
		t.Fatalf("GeneratePrompt = (%q, %v), want (\"看图说话\", true)", prompt, ok)
	}

	// 最后一轮仅包含函数结果（part[11]）且带 system 时，GeneratePrompt 应返回 false 以走函数结果提交流程
	fnResultBody := []byte(`["models/gemini-4-carbon",[[[[null,null,null,null,null,null,null,null,null,null,["fn",{}]]],"model"],[[[null,null,null,null,null,null,null,null,null,null,null,["fn",{"ok":true}]]],"user"]],null,[null,null,null,65536],"!PROOF",[[[null,"系统指令"]],"user"]]`)
	if _, ok := GeneratePrompt(fnResultBody); ok {
		t.Fatal("函数结果轮不应被 GeneratePrompt 判定为普通文本输入")
	}
}

// TestHumanPointerMoves 验证多段二次贝塞尔弧线轨迹、总时长守恒与自然加减速速度比（无中间段瞬移突刺）
func TestHumanPointerMoves(t *testing.T) {
	fromX, fromY := 100, 120
	toX, toY := 620, 480
	moves := humanPointerMoves(fromX, fromY, toX, toY, 180)
	if len(moves) < 5 || len(moves) > 7 {
		t.Fatalf("期望 5~7 段移动轨迹，实际为 %d 段", len(moves))
	}
	total := 0
	prevX, prevY := fromX, fromY
	minSpeed, maxSpeed := math.MaxFloat64, 0.0
	for index, move := range moves {
		dur, ok := move["duration"].(int)
		if !ok || dur < 10 {
			t.Fatalf("第 %d 段 duration 无效: %v", index, move["duration"])
		}
		x, y := move["x"].(int), move["y"].(int)
		segDist := math.Hypot(float64(x-prevX), float64(y-prevY))
		speed := segDist / float64(dur)
		minSpeed = min(minSpeed, speed)
		maxSpeed = max(maxSpeed, speed)
		prevX, prevY = x, y
		total += dur
	}
	if total != 180 {
		t.Fatalf("轨迹总耗时 = %d, want 180", total)
	}
	avgSpeed := math.Hypot(float64(toX-fromX), float64(toY-fromY)) / float64(total)
	if maxSpeed > avgSpeed*2.8 {
		t.Fatalf("轨迹出现中间段瞬移突刺: maxSpeed=%.2f, avgSpeed=%.2f, ratio=%.2f", maxSpeed, avgSpeed, maxSpeed/avgSpeed)
	}
	_ = minSpeed
	last := moves[len(moves)-1]
	if last["x"] != toX || last["y"] != toY {
		t.Fatalf("终点坐标 = (%v, %v), want (%d, %d)", last["x"], last["y"], toX, toY)
	}
}

// TestNextTabActionModelSwitch 验证同文切换模型时不误判为原地 Rerun，而是新开对话
func TestNextTabActionModelSwitch(t *testing.T) {
	turn1User := json.RawMessage(`[[[null,"你好"]],"user"]`)
	turn1Model := json.RawMessage(`[[[null,"你好！有什么可以帮你？"]],"model"]`)
	turn2User := json.RawMessage(`[[[null,"继续"]],"user"]`)

	prev := []json.RawMessage{turn1User}
	// 同模型同内容 -> chatRerun
	if got := nextTabAction("models/gemini-4-carbon", prev, "models/gemini-4-carbon", prev); got != chatRerun {
		t.Fatalf("同模型同内容期望 chatRerun，实际为 %v", got)
	}
	// 切换模型但同内容 -> chatNew（避免在旧模型对话上点 Rerun 却篡改 XHR 模型）
	if got := nextTabAction("models/gemini-4-carbon", prev, "models/gemini-4-argon", prev); got != chatNew {
		t.Fatalf("换模型同内容期望 chatNew，实际为 %v", got)
	}
	// 多轮中途切换模型 -> chatNew
	curr2 := []json.RawMessage{turn1User, turn1Model, turn2User}
	if got := nextTabAction("models/gemini-4-carbon", prev, "models/gemini-4-argon", curr2); got != chatNew {
		t.Fatalf("多轮中途换模型期望 chatNew，实际为 %v", got)
	}
}

// TestSelectTabInPoolAndFunctionTabDirty 验证多标签页亲和选择优先级（matched > empty > fallback）与 dirty 隔离
func TestSelectTabInPoolAndFunctionTabDirty(t *testing.T) {
	turn1User := json.RawMessage(`[[[null,"问题A"]],"user"]`)
	turn1ModelFn := json.RawMessage(`[[[null,null,null,null,null,null,null,null,null,null,["get_weather",{"city":"SH"}]]],"model"]`)
	turn2UserFnRes := json.RawMessage(`[[[null,null,null,null,null,null,null,null,null,null,null,["get_weather",{"temp":22}]]],"user"]`)
	curr := []json.RawMessage{turn1User, turn1ModelFn, turn2UserFnRes}

	dirtyTab := &chatTab{contextID: "tab-dirty", model: "models/gemini-4-carbon", contents: []json.RawMessage{turn1User}, dirty: true}
	emptyTab := &chatTab{contextID: "tab-empty"}
	matchedTab := &chatTab{contextID: "tab-matched", model: "models/gemini-4-carbon", contents: []json.RawMessage{turn1User}}

	// 1. 同时存在 dirty、empty、matched 时，优先命中 matched
	if got := selectTabInPool([]*chatTab{dirtyTab, emptyTab, matchedTab}, "models/gemini-4-carbon", curr); got != matchedTab {
		t.Fatalf("期望优先选中 matchedTab，实际为 %+v", got)
	}
	// 2. 新话题无匹配时，优先选中健康 emptyTab，跳过 dirtyTab
	newTopic := []json.RawMessage{json.RawMessage(`[[[null,"新话题"]],"user"]`)}
	if got := selectTabInPool([]*chatTab{dirtyTab, emptyTab}, "models/gemini-4-carbon", newTopic); got != emptyTab {
		t.Fatalf("新话题期望优先选中 emptyTab，实际为 %+v", got)
	}
	// 3. 仅有 dirtyTab 空闲时，作为 fallback 返回供 startTurn 自愈重置
	if got := selectTabInPool([]*chatTab{dirtyTab}, "models/gemini-4-carbon", newTopic); got != dirtyTab {
		t.Fatalf("仅剩 dirtyTab 时期望返回 fallback，实际为 %+v", got)
	}

	// 4. functionTab 必须拒绝 dirty 或跨模型的标签页
	w := &Worker{chats: []*chatTab{dirtyTab}}
	if tab := w.functionTab("models/gemini-4-carbon", curr); tab != nil {
		t.Fatalf("dirty 标签页不应被 functionTab 命中: %+v", tab)
	}
	w.chats = []*chatTab{matchedTab}
	if tab := w.functionTab("models/gemini-4-argon", curr); tab != nil {
		t.Fatalf("跨模型标签页不应被 functionTab 命中: %+v", tab)
	}
	if tab := w.functionTab("models/gemini-4-carbon", curr); tab != matchedTab {
		t.Fatalf("健康同模型标签页应被 functionTab 命中，实际为 %+v", tab)
	}
}
