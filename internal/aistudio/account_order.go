package aistudio

import (
	"context"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	appconfig "github.com/Mag1cFall/AIStudio2API/internal/config"
)

type accountOrderContextKey struct{}
type modelAliasContextKey struct{}

// ContextWithAccountOrder 将本次请求指定的账户顺序或规则名写入上下文（如 X-AIStudio-Account-Order 请求头）
func ContextWithAccountOrder(ctx context.Context, order string) context.Context {
	return context.WithValue(ctx, accountOrderContextKey{}, strings.TrimSpace(order))
}

func contextAccountOrder(ctx context.Context) string {
	order, _ := ctx.Value(accountOrderContextKey{}).(string)
	return order
}

// ContextWithModelAlias 将 MODEL_MAPPING 转换前的客户端请求模型名写入上下文，供命名顺序规则按别名匹配
func ContextWithModelAlias(ctx context.Context, alias string) context.Context {
	alias = strings.TrimPrefix(strings.TrimSpace(alias), "models/")
	if open := strings.LastIndex(alias, "("); open > 0 && strings.HasSuffix(alias, ")") {
		alias = strings.TrimSpace(alias[:open])
	}
	return context.WithValue(ctx, modelAliasContextKey{}, alias)
}

func contextModelAlias(ctx context.Context) string {
	alias, _ := ctx.Value(modelAliasContextKey{}).(string)
	return alias
}

// SetAccountOrderRules 设置多套定制账户耗尽顺序规则并立即唤醒等待队列
func (p *AccountPool) SetAccountOrderRules(rules []appconfig.AccountOrderRule) {
	cloned := make([]appconfig.AccountOrderRule, len(rules))
	for i, rule := range rules {
		cloned[i] = appconfig.AccountOrderRule{
			Kind:     rule.Kind,
			Target:   rule.Target,
			Accounts: slices.Clone(rule.Accounts),
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accountOrderRules = cloned
	p.notifyLocked()
}

// baseOrderedAccountsLocked 返回按稳定 ID 升序排列的账户列表，作为 1/2/3/4 序号引用的确定性基准；调用方持有 p.mu
func (p *AccountPool) baseOrderedAccountsLocked() []*Account {
	base := slices.Clone(p.accounts)
	sort.Slice(base, func(left, right int) bool {
		return base[left].ID < base[right].ID
	})
	return base
}

// resolveAccountRefLocked 解析账户引用；allowFuzzyOrder 为真时额外允许裸数字序号（1/2/3/4）与唯一邮箱前缀（仅用于顺序规则和请求头，不用于 API_KEY_ACCOUNTS/MODEL_ACCOUNTS 权限边界以防新号加入引起权限漂移）
func (p *AccountPool) resolveAccountRefLocked(token string, base []*Account, allowFuzzyOrder bool) (string, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}
	if account := p.byID[token]; account != nil {
		return account.ID, true
	}
	if base == nil {
		base = p.baseOrderedAccountsLocked()
	}
	if strings.HasPrefix(token, "#") {
		if index, err := strconv.Atoi(strings.TrimSpace(token[1:])); err == nil && index >= 1 && index <= len(base) {
			return base[index-1].ID, true
		}
		return "", false
	}
	for _, account := range base {
		if strings.EqualFold(account.ID, token) {
			return account.ID, true
		}
	}
	labelMatch := ""
	labelHits := 0
	for _, account := range base {
		if label := strings.TrimSpace(account.Config.Label); label != "" && strings.EqualFold(label, token) {
			labelMatch = account.ID
			labelHits++
		}
	}
	if labelHits == 1 {
		return labelMatch, true
	}
	if !allowFuzzyOrder {
		return "", false
	}
	if index, err := strconv.Atoi(token); err == nil && index >= 1 && index <= len(base) {
		return base[index-1].ID, true
	}
	if !strings.Contains(token, "@") {
		prefixMatch := ""
		prefixHits := 0
		for _, account := range base {
			if at := strings.IndexByte(account.ID, '@'); at > 0 && strings.EqualFold(account.ID[:at], token) {
				prefixMatch = account.ID
				prefixHits++
			}
		}
		if prefixHits == 1 {
			return prefixMatch, true
		}
	}
	return "", false
}

// resolveScopeTokensLocked 解析权限范围列表（API_KEY_ACCOUNTS / MODEL_ACCOUNTS）：仅支持精确/忽略大小写 ID、唯一 Label 与 #N，未命中时保留原值以维持严格隔离语义
func (p *AccountPool) resolveScopeTokensLocked(tokens []string, base []*Account, allowFuzzyOrder bool) []string {
	if len(tokens) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(tokens))
	for _, token := range tokens {
		id, ok := p.resolveAccountRefLocked(token, base, allowFuzzyOrder)
		if !ok {
			id = strings.TrimSpace(token)
		}
		if id != "" && !slices.Contains(resolved, id) {
			resolved = append(resolved, id)
		}
	}
	return resolved
}

// resolveOrderTokensLocked 将一组顺序 token 解析为去重后的有效稳定账户 ID 列表（支持 1/2/3/4、#N、唯一 Label、唯一邮箱前缀与完整 ID）
func (p *AccountPool) resolveOrderTokensLocked(tokens []string, base []*Account) []string {
	if len(tokens) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if id, ok := p.resolveAccountRefLocked(token, base, true); ok && !slices.Contains(resolved, id) {
			resolved = append(resolved, id)
		}
	}
	return resolved
}

// AccountMatchesRequest 判断稳定账户 ID 是否与 X-AIStudio-Account 请求头（支持单号简写或多号列表）匹配
func (p *AccountPool) AccountMatchesRequest(accountID string, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return true
	}
	if requested == accountID {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	base := p.baseOrderedAccountsLocked()
	if isMultiAccountHeader(requested) {
		ids := p.resolveScopeTokensLocked(appconfig.ParseAccountOrderTokens(requested), base, true)
		return slices.Contains(ids, accountID)
	}
	resolved, ok := p.resolveAccountRefLocked(requested, base, true)
	return ok && resolved == accountID
}

func isMultiAccountHeader(value string) bool {
	return strings.ContainsAny(strings.TrimSpace(value), ",|>/ \t\n\r")
}

// findNamedRuleLocked 按名称查找命名顺序规则；调用方持有 p.mu
func (p *AccountPool) findNamedRuleLocked(name string) ([]string, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if name == "" {
		return nil, false
	}
	for _, rule := range p.accountOrderRules {
		if rule.Kind == appconfig.OrderRuleNamed && strings.EqualFold(rule.Target, name) {
			return rule.Accounts, true
		}
	}
	return nil, false
}

// expandOrderSpecLocked 展开顺序表达式：支持 @规则名、单 token 命名规则引用（当其不是精确账户 ID 时）或直接账户序列
func (p *AccountPool) expandOrderSpecLocked(spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	if strings.HasPrefix(spec, "@") {
		if accounts, ok := p.findNamedRuleLocked(spec); ok {
			return accounts
		}
	}
	tokens := appconfig.ParseAccountOrderTokens(spec)
	if len(tokens) == 1 && p.byID[tokens[0]] == nil {
		if accounts, ok := p.findNamedRuleLocked(tokens[0]); ok {
			return accounts
		}
	}
	return tokens
}

// defaultOrderTokensLocked 返回全局 ACCOUNT_ORDER 展开后的 token 序列
func (p *AccountPool) defaultOrderTokensLocked() []string {
	if len(p.accountOrderTokens) == 1 {
		token := p.accountOrderTokens[0]
		if strings.HasPrefix(token, "@") || p.byID[token] == nil {
			if accounts, ok := p.findNamedRuleLocked(token); ok {
				return accounts
			}
		}
	}
	return p.accountOrderTokens
}

// primaryOrderTokensLocked 按优先级匹配当前请求显式配置的定制顺序规则（请求头 > key= 规则 > 模型别名/模型规则；API_KEY_ACCOUNTS 与 MODEL_ACCOUNTS 仅限定范围，不静默改变排序优先级）
func (p *AccountPool) primaryOrderTokensLocked(selection AccountSelection) []string {
	if selection.OrderOverride != "" {
		return p.expandOrderSpecLocked(selection.OrderOverride)
	}
	if selection.apiKey != "" {
		for _, rule := range p.accountOrderRules {
			if rule.Kind == appconfig.OrderRuleAPIKey && rule.Target == selection.apiKey {
				return rule.Accounts
			}
		}
	}
	modelID := strings.TrimPrefix(strings.TrimSpace(selection.ModelID), "models/")
	alias := strings.TrimPrefix(strings.TrimSpace(selection.ModelAlias), "models/")
	if alias != "" {
		for _, rule := range p.accountOrderRules {
			if (rule.Kind == appconfig.OrderRuleNamed || rule.Kind == appconfig.OrderRuleModel) && strings.EqualFold(rule.Target, alias) {
				return rule.Accounts
			}
		}
	}
	if modelID != "" {
		for _, rule := range p.accountOrderRules {
			if (rule.Kind == appconfig.OrderRuleModel || rule.Kind == appconfig.OrderRuleNamed) && strings.EqualFold(rule.Target, modelID) {
				return rule.Accounts
			}
		}
	}
	for _, rule := range p.accountOrderRules {
		if rule.Kind != appconfig.OrderRuleModel || !strings.ContainsAny(rule.Target, "*?[") {
			continue
		}
		if alias != "" {
			if matched, _ := path.Match(strings.ToLower(rule.Target), strings.ToLower(alias)); matched {
				return rule.Accounts
			}
		}
		if modelID != "" {
			if matched, _ := path.Match(strings.ToLower(rule.Target), strings.ToLower(modelID)); matched {
				return rule.Accounts
			}
		}
	}
	return nil
}

// effectiveOrderMapLocked 计算当前选择条件下各稳定账户 ID 的优先顺位（先排专属规则，未列出的接全局 ACCOUNT_ORDER，其余返回 math.MaxInt 按 ID 兜底）
func (p *AccountPool) effectiveOrderMapLocked(selection AccountSelection) map[string]int {
	base := p.baseOrderedAccountsLocked()
	primary := p.resolveOrderTokensLocked(p.primaryOrderTokensLocked(selection), base)
	fallback := p.resolveOrderTokensLocked(p.defaultOrderTokensLocked(), base)
	if len(primary) == 0 && len(fallback) == 0 {
		return nil
	}
	ranks := make(map[string]int, len(primary)+len(fallback))
	nextRank := 0
	for _, id := range primary {
		if _, exists := ranks[id]; !exists {
			ranks[id] = nextRank
			nextRank++
		}
	}
	for _, id := range fallback {
		if _, exists := ranks[id]; !exists {
			ranks[id] = nextRank
			nextRank++
		}
	}
	return ranks
}

func rankInOrderMap(ranks map[string]int, accountID string) int {
	if ranks != nil {
		if rank, ok := ranks[accountID]; ok {
			return rank
		}
	}
	return math.MaxInt
}

// SelectionOrderSignature 返回本次请求在当前策略下的有效账户范围与顺序签名，供调度队列区分不同顺序规则或指定账户以避免队头阻塞
func (p *AccountPool) SelectionOrderSignature(ctx context.Context, selection AccountSelection) string {
	selection = p.scopedSelection(ctx, selection)
	allowed := slices.Clone(selection.AllowedAccountIDs)
	slices.Sort(allowed)
	scopePrefix := selection.AccountID + "|" + strings.Join(allowed, ",") + "|"
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.routingStrategy == "round-robin" {
		return scopePrefix + "rr"
	}
	ranks := p.effectiveOrderMapLocked(selection)
	if len(ranks) == 0 {
		return scopePrefix + "default"
	}
	ordered := make([]string, len(ranks))
	for id, rank := range ranks {
		if rank >= 0 && rank < len(ordered) {
			ordered[rank] = id
		}
	}
	return scopePrefix + strings.Join(ordered, ">")
}

// OrderSelectionCandidates 按当前请求上下文与定制顺序规则排列候选账户，不推进轮询游标
func (p *AccountPool) OrderSelectionCandidates(ctx context.Context, selection AccountSelection, accountIDs []string) []string {
	if len(accountIDs) == 0 {
		return nil
	}
	selection = p.scopedSelection(ctx, selection)
	selection.AccountID = ""
	selection.ResourceID = ""
	selection.AllowedAccountIDs = accountIDs
	p.mu.Lock()
	defer p.mu.Unlock()
	indices, _ := p.selectionIndicesLocked(selection)
	ordered := make([]string, 0, len(indices))
	for _, index := range indices {
		ordered = append(ordered, p.accounts[index].ID)
	}
	return ordered
}
