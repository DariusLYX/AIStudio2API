package aistudio

import (
	"context"
	"slices"
	"testing"

	appconfig "github.com/Mag1cFall/AIStudio2API/internal/config"
)

func newTestPoolForOrder(t *testing.T) *AccountPool {
	t.Helper()
	model := Model{ID: "gemini-4-carbon", Methods: []string{"generateContent"}}
	argon := Model{ID: "gemini-4-argon", Methods: []string{"generateContent"}}
	pro := Model{ID: "gemini-2.5-pro", Methods: []string{"generateContent"}}
	makeAcc := func(id, label string) *Account {
		return &Account{
			ID:     id,
			Config: AccountConfig{Label: label, Enabled: true},
			State:  AccountReady,
			Models: []Model{model, argon, pro},
		}
	}
	// 稳定字典序: acc1@gmail.com(#1), acc2@gmail.com(#2), acc3@gmail.com(#3), acc4@gmail.com(#4)
	pool := NewAccountPool([]*Account{
		makeAcc("acc1@gmail.com", "主力A"),
		makeAcc("acc2@gmail.com", "主力B"),
		makeAcc("acc3@gmail.com", "备用C"),
		makeAcc("acc4@gmail.com", "备用D"),
	}, 2)
	pool.SetUpstreamChannels([]Channel{ChannelPlayground})
	pool.SetRoutingStrategy("fill-first")
	return pool
}

// TestCustomAccountOrderRules 验证 1/2/3/4 序号、Label、邮箱前缀、命名预设、model= 通配、key= 绑定与请求头覆盖的完整优先级
func TestCustomAccountOrderRules(t *testing.T) {
	pool := newTestPoolForOrder(t)
	all := []string{"acc1@gmail.com", "acc2@gmail.com", "acc3@gmail.com", "acc4@gmail.com"}

	rules, err := appconfig.ParseAccountOrderRules(
		"daily:1>2>3>4, rev:4>3>2>1, pairB:备用C|备用D|主力A|主力B, " +
			"carbon-3412:3>4>1>2, model=gemini-4-argon:2>4>1>3, model=gemini-4-*:1>3>2>4, key=sk-special:4>1>3>2",
	)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetAccountOrderRules(rules)

	// 1. ACCOUNT_ORDER 直接引用命名预设 @rev -> 4,3,2,1
	pool.SetAccountOrder(appconfig.ParseAccountOrderTokens("@rev"))
	got := pool.OrderSelectionCandidates(context.Background(), AccountSelection{ModelID: "gemini-2.5-pro"}, all)
	if want := []string{"acc4@gmail.com", "acc3@gmail.com", "acc2@gmail.com", "acc1@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("全局命名预设 @rev got=%v, want=%v", got, want)
	}

	// 2. model=gemini-4-argon 精确模型规则优先于 model=gemini-4-* 通配与全局默认 -> 2,4,1,3
	got = pool.OrderSelectionCandidates(context.Background(), AccountSelection{ModelID: "gemini-4-argon"}, all)
	if want := []string{"acc2@gmail.com", "acc4@gmail.com", "acc1@gmail.com", "acc3@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("精确模型规则 got=%v, want=%v", got, want)
	}

	// 3. gemini-4-carbon 命中通配规则 model=gemini-4-* -> 1,3,2,4
	got = pool.OrderSelectionCandidates(context.Background(), AccountSelection{ModelID: "gemini-4-carbon"}, all)
	if want := []string{"acc1@gmail.com", "acc3@gmail.com", "acc2@gmail.com", "acc4@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("模型通配规则 got=%v, want=%v", got, want)
	}

	// 4. 客户端以 MODEL_MAPPING 别名 carbon-3412（含思考后缀或 Claude Code [1m] 后缀）请求时，别名规则优先于目标模型规则 -> 3,4,1,2
	for _, rawAlias := range []string{"models/carbon-3412(high)", "carbon-3412[1m]", "models/carbon-3412(high)[1m]"} {
		aliasCtx := ContextWithModelAlias(context.Background(), rawAlias)
		got = pool.OrderSelectionCandidates(aliasCtx, AccountSelection{ModelID: "gemini-4-carbon"}, all)
		if want := []string{"acc3@gmail.com", "acc4@gmail.com", "acc1@gmail.com", "acc2@gmail.com"}; !slices.Equal(got, want) {
			t.Fatalf("模型别名规则(%s) got=%v, want=%v", rawAlias, got, want)
		}
	}
	aliasCtx := ContextWithModelAlias(context.Background(), "models/carbon-3412(high)")

	// 5. key=sk-special 规则优先于模型规则 -> 4,1,3,2
	keyCtx := ContextWithAPIKey(aliasCtx, "sk-special")
	got = pool.OrderSelectionCandidates(keyCtx, AccountSelection{ModelID: "gemini-4-carbon"}, all)
	if want := []string{"acc4@gmail.com", "acc1@gmail.com", "acc3@gmail.com", "acc2@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("API Key 规则 got=%v, want=%v", got, want)
	}

	// 6. 请求头 X-AIStudio-Account-Order 最高优先级（支持引用 Label 命名预设 pairB 或直接写 2>3）
	hdrCtx := ContextWithAccountOrder(keyCtx, "pairB")
	got = pool.OrderSelectionCandidates(hdrCtx, AccountSelection{ModelID: "gemini-4-carbon"}, all)
	if want := []string{"acc3@gmail.com", "acc4@gmail.com", "acc1@gmail.com", "acc2@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("请求头引用命名规则 pairB got=%v, want=%v", got, want)
	}

	// 部分列出的请求头覆盖（如只指定 2>1），未列出的按全局 @rev (4>3) 排在后面 -> 2,1,4,3
	partialCtx := ContextWithAccountOrder(context.Background(), "acc2>1")
	got = pool.OrderSelectionCandidates(partialCtx, AccountSelection{ModelID: "gemini-4-carbon"}, all)
	if want := []string{"acc2@gmail.com", "acc1@gmail.com", "acc4@gmail.com", "acc3@gmail.com"}; !slices.Equal(got, want) {
		t.Fatalf("部分顺序覆盖 + 全局兜底继承 got=%v, want=%v", got, want)
	}

	// 7. API_KEY_ACCOUNTS 仅限制可用范围，不静默篡改全局 ACCOUNT_ORDER 优先级（M1 回归保护）
	pool.SetAPIKeyAccounts(map[string][]string{"sk-scoped": {"acc3@gmail.com", "acc1@gmail.com"}})
	scopedKeyCtx := ContextWithAPIKey(context.Background(), "sk-scoped")
	groups, err := pool.ClassifyCandidates(scopedKeyCtx, AccountSelection{ModelID: "gemini-2.5-pro"}, nil)
	if err != nil || !slices.Equal(groups.StandbyReady, []string{"acc3@gmail.com", "acc1@gmail.com"}) {
		// 全局 @rev 为 4>3>2>1，与 {acc3, acc1} 交集后应保持 3>1；若改全局为 daily(1>2>3>4) 则应变为 1>3
		t.Fatalf("API_KEY_ACCOUNTS 范围过滤 under @rev got=%v err=%v", groups.StandbyReady, err)
	}
	pool.SetAccountOrder(appconfig.ParseAccountOrderTokens("@daily"))
	groups, err = pool.ClassifyCandidates(scopedKeyCtx, AccountSelection{ModelID: "gemini-2.5-pro"}, nil)
	if err != nil || !slices.Equal(groups.StandbyReady, []string{"acc1@gmail.com", "acc3@gmail.com"}) {
		t.Fatalf("API_KEY_ACCOUNTS 不应覆盖全局 @daily 顺序: got=%v err=%v", groups.StandbyReady, err)
	}

	// 8. 多号 X-AIStudio-Account 请求头（支持空格或逗号分隔）与 API_KEY_ACCOUNTS 取交集 + SelectionOrderSignature 隔离 + AccountMatchesRequest
	multiHdrCtx := ContextWithAccount(scopedKeyCtx, "3 2 1")
	groups, err = pool.ClassifyCandidates(multiHdrCtx, AccountSelection{ModelID: "gemini-2.5-pro"}, nil)
	if err != nil || !slices.Equal(groups.StandbyReady, []string{"acc3@gmail.com", "acc1@gmail.com"}) {
		t.Fatalf("多号请求头与 API_KEY_ACCOUNTS 交集及顺序 got=%v err=%v", groups.StandbyReady, err)
	}
	sig1 := pool.SelectionOrderSignature(ContextWithAccount(context.Background(), "1"), AccountSelection{ModelID: "gemini-4-carbon"})
	sig2 := pool.SelectionOrderSignature(ContextWithAccount(context.Background(), "2"), AccountSelection{ModelID: "gemini-4-carbon"})
	if sig1 == sig2 {
		t.Fatalf("不同单号请求头应生成不同排队签名: sig1=%q sig2=%q", sig1, sig2)
	}
	if !pool.AccountMatchesRequest("acc1@gmail.com", "1") || !pool.AccountMatchesRequest("acc1@gmail.com", "主力A") ||
		!pool.AccountMatchesRequest("acc3@gmail.com", "3,4") || pool.AccountMatchesRequest("acc2@gmail.com", "3,4") {
		t.Fatal("AccountMatchesRequest 简写与多号匹配异常")
	}
}
