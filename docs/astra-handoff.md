# Mac 端双号常驻 & 真人网页操作版 — Astra 交接与维护指南

本文档面向在 macOS 上维护本仓库的 AI Agent（Astra），说明本 Fork（`https://github.com/DariusLYX/AIStudio2API`）相对于上游 `Mag1cFall/AIStudio2API` 的定制改造、经过 Opus 5.5 两轮审查收敛后的设计不变量、Mac 部署配置以及日常维护注意事项。

---

## 一、 核心业务场景与设计目标

1. **自制前端 UI + 长记忆携带 + 偶发贴图**：
   - 用户使用自制聊天 UI，每次对话携带本地整理的记忆库（放在正文或 `system` 字段中，长度可能超过 25,000 字），并会不定期上传图片。
2. **解决网页端切后台断流痛点**：
   - 浏览器直接打开 AI Studio 切到后台会因 Background Tab Throttling 导致生成中断失败；通过本服务在后台驱动独立 Camoufox 实例，使 AI Studio 标签页在浏览器内部全天候保持前台 60fps 激活与焦点态。
3. **双号常驻、单线程真人节奏、严禁误触 Build 通道**：
   - 后台常驻约 2 个 Google 账号，只走官网 `playground` 通道（绝不使用注入 `ask_question` 提示词的 `build` 通道）。
4. **关于 AI Studio 云端聊天记录**：
   - Worker 打开页面时固定携带 `?model=...&temporary=true`（官网无痕 Temporary Chat 模式，页面顶部带 `<ms-incognito-mode-indicator>` 标识），而正在进行中的多轮对话会保留在当前 Camoufox 页面中，直到开启新话题才点击 `New chat`。

---

## 二、 相对上游的关键代码改造清单（已过 Opus 5.5 两轮审查收敛）

| 模块与文件 | 改造点 | 机制说明 |
| :--- | :--- | :--- |
| **全时 60Hz 前台渲染 + 空闲 GC 保留**<br>`internal/camoufoxnative/worker.go` | 保持 `frame-rate-divisor = 1` 与 `renderLayers = true` | 上游在 `waitSent`（请求刚发出、流式生成尚在进行中）即把无头浏览器降为 1 FPS 并设置 `renderLayers = false`。现改为全时保持 60Hz 与 `renderLayers = true`，同时保留 `worker.idle` 5 分钟空闲 `memory-pressure` GC 回收，兼顾 7×24 常驻内存健康与流式渲染一致性。 |
| **生物特征随机化**<br>`internal/camoufoxnative/page.go` | 点击坐标随机分布 + 移动/按键时长抖动 | `pointerTargetFunction` 从死锁元素几何正中心 `(0.5, 0.5)` 改为 `[0.3, 0.7]` 安全区随机落点；鼠标移动耗时（`95~214ms`）、点击按压（`45~89ms`）与 `Ctrl+V`/`Ctrl+A` 按键停顿（`shortcutKeyActions`）均改为随机分布，且 100% 走 WebDriver BiDi 物理可信输入（`isTrusted: true`）。 |
| **多轮原地续聊与脏状态自愈**<br>`internal/camoufoxnative/generate.go` | 取消每轮结束自动 `resetChat` + `tab.dirty` 与 `chatEmptyExpression` 校验 | 1. `releaseChat` 不再在每轮普通回答结束后 0 秒秒点 `New chat` 清空页面，对话保留在当前页；下一轮到来时 `nextChatAction` 自动判定 `chatContinue`，在原页面底部继续输入。<br>2. **脏状态自愈（B1 & N1 修复）**：若上一轮 `waitSent` 失败或上游返回非 200，标记 `tab.dirty = true`；下一轮 `startTurn` 时实测页面 `chatEmptyExpression`，若页面已空则直接清除 `dirty` 并走 `chatContinue`，若页面非空且需要重开则执行 `newChat` 清空。<br>3. **流式分块与非文本 Part 精确匹配（H5 / M2 / N2 修复）**：`visibleContentText` 跳过 `part[12]` 思考块并使用无空格拼接 `strings.Join(values, "")` 对齐流式分块还原的模型回复；`nonTextTurnParts` 剥离 `part[1]`（文本）、`part[12]`（思考）与 `part[14]`（思考签名）后严格比对图片/文件/函数调用（`part[10]`）与函数结果（`part[11]`）。 |
| **长记忆与图片无损送达**<br>`internal/camoufoxnative/generate.go` | `promptHead` 可信粘贴 + 完整正文与同出口图片挂载 | 输入框以 `promptHead`（最后一轮前 25,000 字）执行物理 `Ctrl+V` 粘贴（与页面伴随 `CountTokens` 严格一致，且不触发 `>25k` 字转附件阻塞），任意长度的完整长记忆及经同账户 Firefox 158 指纹上传的图片引用通过请求正文完整发出。 |
| **双号多轮会话粘性路由**<br>`internal/aistudio/runtime_native.go`<br>`internal/app/runtime.go`<br>`internal/camoufoxnative/generate.go` | `PageChatAccount` + `acquireChat` 标签页亲和匹配 | 1. 多轮对话进入调度时，若无内联媒体优先绑定，`PageChatAccount` 检查哪个账户的 Camoufox 标签页正好停留在上一轮对话（`ContinuesChat`），将其设为 `preferredAccountID`。<br>2. `acquireChat` 优先选取可续接当前 `contents` 的标签页，其次选取空白标签页。 |

---

## 三、 macOS 部署与 `.env` 推荐配置

在 Mac 上拉取本仓库后，按如下 `.env` 配置运行（双号常驻 + Camoufox 模式）：

```ini
# 强制使用 Camoufox 浏览器后端
WAA_BACKEND=camoufox

# 双号常驻预热：2 个号保持常驻，单号允许 2 并发（多并发或多会话交替时自动在同浏览器开新 Tab 并保持各 Tab 历史）
WARM_WORKER_LIMIT=2
MAX_ACTIVE_WORKERS=2
PER_ACCOUNT_CONCURRENCY=2

# 账号粘性优先（配合 PageChatAccount + acquireChat 多标签页亲和匹配，多对话同时交替聊也不会互相冲掉）
ROUTING_STRATEGY=fill-first

# 关闭首字超时并发备用请求（避免一个问题同时在两个号上点 Run）
FIRST_EVENT_HEDGE=off

# 只启用官网 Playground 通道，禁用 Build 原生非流式优先（彻底隔离 Build 通道）
UPSTREAM_CHANNELS=playground
BUILD_NATIVE_NONSTREAM=false
```

### Mac 构建与启动命令

1. **直接基于源码构建（macOS）**：
   参见根目录 `README.md` 的「方式三：Linux 与 macOS 源码构建」，需要 Xcode Command Line Tools、Go 1.27.3+、Node.js 22+，并下载对应架构（`darwin-arm64` 或 `darwin-amd64`）的 `spidermonkey` 与 `libwebp` 静态库后执行：
   ```bash
   cd web && npm ci && npm run build && cd ..
   export CGO_CPPFLAGS="-I$PWD/spidermonkey/include -I$PWD/libwebp/include"
   export CGO_LDFLAGS="-L$PWD/spidermonkey/lib -L$PWD/libwebp/lib"
   go build -o aistudio2api ./cmd/aistudio2api
   ```
2. **添加两个 Google 账号**：
   ```bash
   ./aistudio2api setup --login
   ```
   登录会自动打开隔离的 Camoufox 窗口，登录并完成 Drive 授权后保存至 `auth/<邮箱>/`。

---

## 四、 Astra 后续维护与合并上游指南

1. **同步上游改动**：
   ```bash
   git remote add upstream https://github.com/Mag1cFall/AIStudio2API.git
   git fetch upstream
   ```
   合并 `upstream/main` 时，重点保护上述改造点（集中在 `internal/camoufoxnative/worker.go`、`page.go`、`generate.go`、`internal/aistudio/runtime_native.go` 与 `internal/app/runtime.go`）。
2. **回归测试**：
   每次修改或合并后运行：
   ```bash
   go test -v ./internal/camoufoxnative/... ./internal/config/...
   ```
   确保全部测试通过。
