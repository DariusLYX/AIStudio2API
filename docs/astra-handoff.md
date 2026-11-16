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
   - ⚠️ **给 Astra 的特别说明**：用户提到你在 Mac 本地已经帮他改过这套“尽力用完前一个号，除非额度用完或账号报错才切后面的号”的逻辑。本仓库已在 `internal/aistudio/accounts.go` 的 `classifyCandidatesLocked` 与 `tryAcquireLocked`（当 `ROUTING_STRATEGY=fill-first` 时）实装了该语义；你在 Mac 本地拉取/合并时，**请对比你本地已有的多号顺序切换改动，直接保留/融合你本地已验证稳定的账号切换与预热参数**。
4. **严禁误触 Build 通道**：
   - 只走官网 `playground` 通道，禁用注入 `ask_question` 提示词的 `build` 通道（`BUILD_NATIVE_NONSTREAM=false`）。

---

## 二、 相对上游的关键代码改造清单（已过 Opus 5.5 两轮审查收敛）

| 模块与文件 | 改造点 | 机制说明 |
| :--- | :--- | :--- |
| **全时 60Hz 前台渲染 + 空闲 GC 保留**<br>`internal/camoufoxnative/worker.go` | 保持 `frame-rate-divisor = 1` 与 `renderLayers = true` | 上游在 `waitSent`（请求刚发出、流式生成尚在进行中）即把无头浏览器降为 1 FPS 并设置 `renderLayers = false`。现改为全时保持 60Hz 与 `renderLayers = true`，同时保留 `worker.idle` 5 分钟空闲 `memory-pressure` GC 回收，兼顾 7×24 常驻内存健康与流式渲染一致性。 |
| **生物特征与阅读浏览模拟**<br>`internal/camoufoxnative/page.go`<br>`internal/camoufoxnative/bidi.go`<br>`internal/camoufoxnative/generate.go` | 多段贝塞尔弧线鼠标轨迹 + 键鼠切换生理停顿 + 空闲期异步滚轮浏览 | 1. `humanPointerMoves` 将鼠标移动拆分为 5~7 段二次贝塞尔微弧度轨迹，配合 Smoothstep 缓入缓出（慢→快→慢对准）加减速。<br>2. 点击输入框到 `Ctrl+V`（`65~130ms`）、`Ctrl+V` 结束到移动鼠标点 `Run`（`110~230ms`）加入人类手眼协调微停顿。<br>3. `simulatePostTurnReading` 在每轮回答结束后的空闲期（延迟 `0.9~2.2s`，通过 `inputMu.TryLock()` 非阻塞让路，对响应 0 延迟）异步将鼠标从 `Run` 按钮沿弧线移回对话阅读区，并执行 `wheel` 滚轮上下轻滚浏览。 |
| **多轮原地续聊、多标签页并发与脏状态自愈**<br>`internal/camoufoxnative/generate.go` | 取消每轮结束自动 `resetChat` + `tab.dirty` 与 `chatEmptyExpression` 校验 + `acquireChat` 亲和匹配 | 1. `releaseChat` 不再在每轮普通回答结束后 0 秒秒点 `New chat` 清空页面，对话保留在当前标签页；下一轮到来时 `nextChatAction` 自动判定 `chatContinue`，在原页面底部继续输入。<br>2. 同账号并发跑 2 个请求或交替聊多个话题时，`acquireChat` 优先选取可续接当前 `contents` 的标签页，全部忙碌时自动在同浏览器新建第 2 个标签页（Multi-Tab）并行运行。<br>3. **脏状态自愈（B1 & N1 修复）**：若上一轮 `waitSent` 失败或上游返回非 200，标记 `tab.dirty = true`；下一轮 `startTurn` 时实测页面 `chatEmptyExpression`，若页面已空则直接清除 `dirty` 并走 `chatContinue`，若页面非空且需要重开则执行 `newChat` 清空。<br>4. **流式分块与非文本 Part 精确匹配（H5 / M2 / N2 修复）**：`visibleContentText` 跳过 `part[12]` 思考块并使用无空格拼接 `strings.Join(values, "")` 对齐流式分块还原的模型回复；`nonTextTurnParts` 剥离 `part[1]`（文本）、`part[12]`（思考）与 `part[14]`（思考签名）后严格比对图片/文件/函数调用（`part[10]`）与函数结果（`part[11]`）。 |
| **多号严格顺序耗尽调度（1→2→3→4 号）**<br>`internal/aistudio/accounts.go` | `fill-first` 严格锁死当前顺位首号 | 在 `classifyCandidatesLocked` 与 `tryAcquireLocked` 中，当 `ROUTING_STRATEGY=fill-first` 时，按 `ACCOUNT_ORDER`（及账户 ID 排序）找到第一个未冷却且狀態为 `ready` 的账户后立即 `break` 截断后续候选；即便该账户当前并发槽位满也优先排队等待它，**绝不溢出到第 2、3、4 个号，除非当前号因额度耗尽或账号错误进入冷却/不可用状态**。 |

---

## 三、 macOS 部署与 `.env` 推荐配置（支持 2~4+ 个号）

```ini
# 强制使用 Camoufox 浏览器后端
WAA_BACKEND=camoufox

# 单账号并发槽位数（设为 2 时，同一个号可开 2 个标签页同时跑 2 个并发请求并保持 2 个活跃对话不互冲）
PER_ACCOUNT_CONCURRENCY=2

# 严格按序耗尽策略：优先死磕第 1 个号，除非额度用完或报错才切第 2、3、4 个号
ROUTING_STRATEGY=fill-first

# 可选：显式指定 1、2、3、4 号的使用先后顺序（留空则按邮箱字母序依次耗尽）
# ACCOUNT_ORDER=first@gmail.com,second@gmail.com,third@gmail.com,fourth@gmail.com

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
2. **运行单元测试验证**：
   ```bash
   go test -v ./internal/camoufoxnative/... ./internal/config/...
   ```
   确认全部用例 PASS 后重新编译 `aistudio2api` 并重启服务。
