package camoufoxnative

import (
	"encoding/json"
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

// TestHumanPointerMoves 验证多段二次贝塞尔弧线轨迹与慢-快-慢时间分配
func TestHumanPointerMoves(t *testing.T) {
	moves := humanPointerMoves(100, 120, 620, 480, 180)
	if len(moves) < 5 || len(moves) > 7 {
		t.Fatalf("期望 5~7 段移动轨迹，实际为 %d 段", len(moves))
	}
	total := 0
	for index, move := range moves {
		dur, ok := move["duration"].(int)
		if !ok || dur < 10 {
			t.Fatalf("第 %d 段 duration 无效: %v", index, move["duration"])
		}
		total += dur
	}
	if total != 180 {
		t.Fatalf("轨迹总耗时 = %d, want 180", total)
	}
	last := moves[len(moves)-1]
	if last["x"] != 620 || last["y"] != 480 {
		t.Fatalf("终点坐标 = (%v, %v), want (620, 480)", last["x"], last["y"])
	}
}


