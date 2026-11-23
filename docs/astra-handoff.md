# Mac 端多号顺序耗尽 & 真人网页操作版 — Astra 交接与维护指南

本文档面向在 macOS 上维护本仓库的 AI Agent（Astra），说明本 Fork（`https://github.com/DariusLYX/AIStudio2API`）相对于上游 `Mag1cFall/AIStudio2API` 的定制改造、经过 Opus 5.5 两轮审查收敛后的设计不变量、**与 Astra 在 Mac 本地既有“多号顺序耗尽”改动的对齐方式**，以及日常维护注意事项。

---

## 一、 核心业务场景与设计目标

1. **自制前端 UI + 长记忆携带 + 偶发贴图**：
   - 用户使用自制聊天 UI，每次对话携带本地整理的记忆库（放在正文或 `system` 字段中，长度可能超过 25,000 字），并会不定期上传图片。
2. **解决网页端切后台断流痛点**：
   - 浏览器直接打开 AI Studio 切到后台会因 Background Tab Throttling 导致生成中断失败；通过本服务在后台驱动独立 Camoufox 实例，使 AI Studio 标签页在浏览器内部全天候保持前台 60fps 激活与焦点态。
3. **支持 2~4+ 个账号严格按序耗尽（Drain-First / Waterfall）**：
   - 后台可挂 2 个、3 个、4 个或更多 Google 账号。
   - **核心调度铁律**：**永远优先死磕排在最前面的第 1 个可用账号（包含并发跑 2 个请求也在该账号的多标签页内完成），绝不因为并发或轮询去动后面的账号；只有当前账号因为额度用尽（429 进入 `cooldown`）或账号报错/受限（401/403/5xx 换号冷却）时，才顺位切到第 2 个号；第 2 个号用完再切第 3 个号、第 4 个号**。
   - ⚠️ **给 Astra 的特别说明**：用户提到你在 Mac 本地已经帮他改过这套“尽力用完前一个号，除非额度用完或账号报错才切后面的号”的逻辑。本仓库已在 `internal/app/runtime.go` 的 `restrictFillFirstGroups`、`internal/aistudio/accounts.go` 的 `tryAcquireLocked` 与 `internal/aistudio/account_order.go` 的 `ACCOUNT_ORDER_RULES` 多规则引擎中实装了该语义；你在 Mac 本地拉取/合并时，**请对比你本地已有的多号顺序切换改动，直接保留/融合你本地已验证稳定的账号切换与预热参数**。
4. **严禁误触 Build 通道**：
   - 只走官网 `playground` 通道，禁用注入 `ask_question` 提示词的 `build` 通道（`BUILD_NATIVE_NONSTREAM=false`）。

---

## 二、 相对上游的关键代码改造清单（已同步至上游 `v0.3.14` `ae59eba` 并经 GPT-6-Astra 审查收敛）

| 模块与文件 | 改造点 | 机制说明 |
| :--- | :--- | :--- |
| **全时 60Hz 前台渲染 + 空闲 GC 保留**<br>`internal/camoufoxnative/worker.go` | 保持 `frame-rate-divisor = 1` 与 `renderLayers = true` | 上游在 `waitSent`（请求刚发出、流式生成尚在进行中）即把无头浏览器降为 1 FPS 并设置 `renderLayers = false`。现改为全时保持 60Hz 与 `renderLayers = true`，同时保留 `worker.idle` 5 分钟空闲 `memory-pressure` GC 回收与 `worker.settle` 2 秒启动静置定时器，兼顾 7×24 常驻内存健康与流式渲染一致性。 |
| **生物特征与阅读浏览模拟**<br>`internal/camoufoxnative/page.go`<br>`internal/camoufoxnative/bidi.go`<br>`internal/camoufoxnative/generate.go`<br>`internal/camoufoxnative/function.go` | 多段贝塞尔弧线鼠标轨迹 + 视口交集采样 + 空闲期分段让路阅读浏览 + 上游 `rhythm` 与富链接 `Ctrl+Shift+V` 融合 | 1. `humanPointerMoves` 将鼠标移动拆分为 5~7 段二次贝塞尔微弧度轨迹，配合空间 Smoothstep 缓入缓出与均匀微扰步长生成自然钟形移速；按标签页独立维护鼠标坐标并在首次移动时同步视口随机进入起点；`pointerTargetFunction` 对目标元素与视口求交集后采样，防止超长回答悬停/Rerun 越出视口。<br>2. 点击输入框/函数响应框到粘贴、粘贴结束到移鼠标点击 `Run`/提交按钮均加入人类手眼协调微停顿；同时融合上游 `v0.3.14` 账户级稳定按键节律 `rhythm.Rhythm` 与 `pasteLinkPattern`（含 YouTube/Google Docs/Drive/Figma 链接时改按 `Ctrl+Shift+V` 纯文本粘贴防止转内联卡片）。<br>3. `simulatePostTurnReading` 在每轮回答结束后的空闲期（延迟 `0.9~2.2s`，通过 `inputMu.TryLock()` 与分段 `inputWaiters` 检查主动让路，对本轮响应返回 0 延迟）异步将鼠标移回对话阅读区并执行带微过冲余量的滚轮浏览。 |
| **多轮原地续聊、多标签页并发与脏状态自愈**<br>`internal/camoufoxnative/generate.go`<br>`internal/camoufoxnative/function.go`<br>`internal/camoufoxnative/worker.go` | 取消每轮结束自动 `resetChat` + 全链路异常 `tab.dirty` 标记 + 模型一致性与后台异步补建标签页 | 1. `releaseChat` 不再在每轮普通回答结束后 0 秒秒点 `New chat` 清空页面，对话保留在当前标签页；下一轮到来时 `nextTabAction` 结合模型名与 `contents` 判定 `chatContinue`，同文换模型自动走 `chatNew` 防止误触发原地 Rerun。<br>2. 同账号并发跑 2 个请求或交替聊多个话题时，`acquireChat` 按 `matched > empty > fallback`（`selectTabInPool`）优先选取可续接当前对话的健康标签页，全部忙碌时复用上游后台 `openChat()` + `chatsChanged` 异步新建标签页并支持请求取消唤醒；`Worker.Reload()` 会完整退役/关闭全部旧标签页并重置池。<br>3. **全链路脏状态自愈**：`startTurn`、`waitSent`、`readResponseHead`、非 200 响应及流式读取中途未完成关闭（`!completed`）均标记 `tab.dirty = true`（`functionTab` 同步拒绝脏标签页并返回 `ErrFunctionTabUnavailable` 回退完整历史重跑）；下一轮实测 `chatEmptyExpression`，非空则执行 `newChat`（兼容残留确认框直接点击）自愈清空；同时合入上游 `clickArmed` 请求取消拦截与 `turnInputTimeout`。<br>4. **流式分块与非文本 Part 精确匹配**：`visibleContentText` 跳过 `part[12]` 思考块并无空格拼接对齐流式分块回复；`nonTextTurnParts` 剥离 `part[1]`/`part[12]`/`part[14]` 后严格比对图片/文件/函数调用（`part[10]`）与函数结果（`part[11]`）。 |
| **多号严格顺序耗尽调度（1→2→3→4 号）与活跃会话粘性**<br>`internal/aistudio/accounts.go`<br>`internal/aistudio/account_order.go`<br>`internal/aistudio/runtime_native.go`<br>`internal/app/runtime.go`<br>`internal/app/worker_manager.go`<br>`internal/app/dispatch.go` | `restrictFillFirstGroups` 首号收敛 + 冷启动故障指数退避 + 多轮续聊亲和 + `[1m]` 后缀规则归一 | 1. `ClassifyCandidates` 保持完整顺位分类（保障 `WARM_WORKER_LIMIT` 后台预热与全冷却排队正常）；非登录类冷启动失败记入跨请求指数退避（30s~5m），`acquireWarmLease` 在过滤退避与失败账号后，**先于上游 `WithoutPendingProofs` 让路**通过 `restrictFillFirstGroups` 严格锁定顺位第 1 个可用账户（`generateContent` 满槽也在该号排队，同号并发通过多标签页消化，绝不因并发在途 Proof 溢出到第 2、3、4 号；首号 429 冷却、报错或冷启动退避时立即顺位切到第 2 号）。<br>2. 若某已活跃 Warm Worker 正持有当前多轮续聊会话（`PageChatAccount` 或 `len(contents)>1` 的已上传内联图片对话），优先保持在该账户聊完当前话题，新开首轮话题立即回到顺位第 1 号。<br>3. 同步合并上游请求体积 `RequestSize` 429 额度分档追踪、首字与流式空闲独立超时、步骤进度上报及 Claude Code `[1m]` 后缀支持（`normalizeModelRuleKey` 自动剥离 `[1m]` 与思考括号，确保 `ACCOUNT_ORDER_RULES` 别名/模型规则在 `[1m]` 模式下精准生效）。 |

---

## 三、 macOS 部署与 `.env` 推荐配置（支持 2~4+ 个号）

```ini
# 强制使用 Camoufox 浏览器后端
WAA_BACKEND=camoufox

# 单账号并发槽位数（设为 2 时，同一个号可开 2 个标签页同时跑 2 个并发请求并保持 2 个活跃对话不互冲）
PER_ACCOUNT_CONCURRENCY=2

# 严格按序耗尽策略：优先死磕第 1 个号，除非额度用完或报错才切第 2、3、4 个号
ROUTING_STRATEGY=fill-first

# 可选：默认账号耗尽顺序（1/2/3/4 为按账户 ID 字典序排列的第 1~4 个号，也支持 #1/#2、唯一账户标签 Label、完整 ID，或填下方规则名如 @main；建议给 4 个账户设置固定 Label 如 1/2/3/4，新增账号亦不漂移）
ACCOUNT_ORDER=1,2,3,4

# 可选：多套定制 1/2/3/4 号顺序规则（保存后立即热生效，无需重启）
#   - 命名预设：main:1>2>3>4, rev:4>3>2>1, pairB:3>4>1>2（可在 ACCOUNT_ORDER=@rev、同名 MODEL_MAPPING 模型别名、或请求头 X-AIStudio-Account-Order: rev 中随时切换）
#   - 模型专属：model=gemini-4-argon:3>4>1>2, model=gemini-4-*:2>1>3>4
#   - 密钥专属：key=sk-chat:1>2>3>4, key=sk-batch:4>3>2>1
# ACCOUNT_ORDER_RULES=main:1>2>3>4,rev:4>3>2>1,model=gemini-4-argon:3>4>1>2

# 常驻预热与最大 Worker 数（默认 Camoufox 为 5 和 10，天然支持挂 2~5 个号；若 Astra 本地已调优按需预热策略，请保留 Astra 本地设置）
# WARM_WORKER_LIMIT=1
# MAX_ACTIVE_WORKERS=4

# 关闭首字超时并发备用请求（避免一个问题同时在两个号上点 Run）
FIRST_EVENT_HEDGE=off

# 只启用官网 Playground 通道，禁用 Build 原生非流式优先（彻底隔离 Build 通道）
UPSTREAM_CHANNELS=playground
BUILD_NATIVE_NONSTREAM=false
```

---

## 四、 Astra 交接与合并执行步骤

1. **拉取本仓库并与 Mac 本地改动对齐**：
   - 本仓库远端：`https://github.com/DariusLYX/AIStudio2API.git`（`main` 分支）。
   - 如果 Mac 本地已有正在运行的目录与 Astra 之前修改过的“多号顺序耗尽/故障切换”代码，请对比 `internal/aistudio/accounts.go` 与 `internal/app/`，将本仓库 `internal/camoufoxnative/` 的全套拟真改造（`worker.go`、`page.go`、`generate.go`、`page_test.go`）与你本地的多号调度无缝合入。
2. **运行单元测试与编译前端/后端**：
   ```bash
   # 如需在 Web 管理后台显示新增的“定制顺序规则”输入框，先构建前端静态产物（不构建前端亦完全兼容，后端保存设置时会自动保留 .env 中的 ACCOUNT_ORDER_RULES）
   (cd web && npm run build) && cp -r web/dist internal/webui/dist
   # 运行后端单元测试
   go test -v ./internal/camoufoxnative/... ./internal/config/... ./internal/aistudio/...
   ```
   确认全部用例 PASS 后重新编译 `aistudio2api` 并重启服务。
