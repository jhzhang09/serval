# Graph Report - Serval  (2026-11-09)

## Corpus Check
- 80 files · ~68,100 words
- Verdict: corpus is large enough that graph structure adds value.
- Unclassified: 4 file(s) not represented in the graph (top: (none) 3, .ttf 1)

## Summary
- 426 nodes · 873 edges · 20 communities (18 shown, 2 thin omitted)
- Extraction: 81% EXTRACTED · 19% INFERRED · 0% AMBIGUOUS · INFERRED: 165 edges (avg confidence: 0.87)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `3f42e5d3`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- app
- app.go
- tab
- client.go
- Server
- 数据存储与同步设计
- Host
- 架构
- 开发计划
- .askChallenge
- C 连接
- F 基础
- R 发布
- P 文件（SFTP）
- T 终端
- S 同步
- AGENTS.md
- totp.go

## God Nodes (most connected - your core abstractions)
1. `app` - 118 edges
2. `tab` - 34 edges
3. `Host` - 33 edges
4. `Shell` - 19 edges
5. `pane` - 18 edges
6. `开发计划` - 16 edges
7. `Config` - 16 edges
8. `Server` - 16 edges
9. `Client` - 15 edges
10. `Dial()` - 13 edges

## Surprising Connections (you probably didn't know these)
- `界面结构` --references--> `app`  [INFERRED]
  docs/architecture.md → app.go
- `C2 多步验证（keyboard-interactive）（M，已完成：`feat/C2-keyboard-interactive`）` --references--> `Config`  [INFERRED]
  docs/roadmap.md → core/sshconn/client.go
- `应用层（package main）` --references--> `app`  [INFERRED]
  docs/architecture.md → app.go
- `扩展点` --references--> `app`  [INFERRED]
  docs/architecture.md → app.go
- `线程模型` --references--> `app`  [INFERRED]
  docs/architecture.md → app.go

## Import Cycles
- None detected.

## Communities (20 total, 2 thin omitted)

### Community 0 - "app"
Cohesion: 0.05
Nodes (3): app, hostForm, hostKeyPrompt

### Community 1 - "app.go"
Cohesion: 0.07
Nodes (19): choose(), looksLikeOTP(), newApp(), serveTestAgent(), TestAgentForwardingVisibleThroughJump(), TestForwardAgentRequiresLocalAgent(), TestFirstSecretAsksForMasterPassword(), TestFormValidation() (+11 more)

### Community 2 - "tab"
Cohesion: 0.06
Nodes (6): firstLeaf(), reconnectRetryable(), savePane(), Shell, pane, tab

### Community 3 - "client.go"
Cohesion: 0.12
Nodes (15): formatLatency(), Proxy, authMethods(), Dial(), dialThrough(), Client, Config, handshake() (+7 more)

### Community 4 - "Server"
Cohesion: 0.09
Nodes (8): Options, Server, NewSigner(), proxyAgentChannel(), serveAgentSocket(), shellEnv(), Start(), TestTOTPAutoFillsChallenge()

### Community 5 - "数据存储与同步设计"
Cohesion: 0.12
Nodes (17): Serval, 主机管理, 参与开发, 安全, 密钥, 尚未验证, 已知问题, 快捷键 (+9 more)

### Community 6 - "Host"
Cohesion: 0.13
Nodes (10): fillSaved(), hasProxy(), hopConfig(), hostVisible(), Host, 应用层（package main）, 扩展点, 连接一台主机 (+2 more)

### Community 7 - "架构"
Cohesion: 0.14
Nodes (11): 依赖, 保存秘密, 关键数据流, 写数据, 启动, 总览, 架构, 测试架构 (+3 more)

### Community 8 - "开发计划"
Cohesion: 0.17
Nodes (12): M1 核心库的 gomobile 可行性（M）, M 移动端, N1 端口转发（M）, N 网络, 可以并行的任务, 已完成（0.1.0）, 开发计划, 当前进度（2026-11-08 第一批验收后） (+4 more)

### Community 9 - ".askChallenge"
Cohesion: 0.33
Nodes (4): autoTOTP(), Question, challengeResult, challengeSheet

### Community 10 - "C 连接"
Cohesion: 0.25
Nodes (8): C1 快速连接（S，已完成：`feat/C1-quick-connect`）, C2 多步验证（keyboard-interactive）（M，已完成：`feat/C2-keyboard-interactive`）, C3 TOTP 自动填充（M，feat/C3-totp）, C4 自动重连与会话恢复（M，已完成：`feat/C4-auto-reconnect`）, C5 本地终端（S，已完成：`feat/C5-local-terminal`）, C6 ssh-agent 转发（S，feat/C6-agent-forward）, C7 证书与硬件密钥（L，需决策）, C 连接

### Community 11 - "F 基础"
Cohesion: 0.25
Nodes (8): F1 正式的应用标识符（S，已完成：`feat/F1-app-identifier`）, F2 设置存储（M，已完成：`feat/F2-settings`）, F3 修改主密码、手动锁定、自动锁定（M，已完成：feat/F3-password-lock）, F4 主密码派生移出主线程（S，已完成：`feat/F4-async-kdf`）, F5 已知主机管理（S，已完成：`feat/F5-known-hosts`）, F6 主机改名时更新跳板机引用（S，已完成：`feat/F6-rename-jumps`）, F7 主机搜索与排序（S，feat/F7-host-search）, F 基础

### Community 12 - "R 发布"
Cohesion: 0.25
Nodes (8): N2 代理独立管理与跳板机选择（M，已完成：`feat/N2-jump-and-proxy`）, R1 Windows（M）, R2 Linux（M）, R3 签名与公证（S，需决策）, R4 自动更新（M）, R5 英文界面（M）, R6 性能基准重测（S）, R 发布

### Community 13 - "P 文件（SFTP）"
Cohesion: 0.29
Nodes (7): P1 上传与下载（M，已完成：`feat/P1-upload-download`）, P2 文件操作（S，已完成：feat/P2-file-operations）, P3 拖放（M，部分完成：拖到终端上传文件和文件夹，`feat/P6-zmodem`、`feat/P3-folder-upload`）, P4 内置编辑器（L）, P5 双栏文件浏览（M）, P6 终端里的 rz/sz（ZMODEM）（M，已完成：`feat/P6-zmodem`）, P 文件（SFTP）

### Community 14 - "T 终端"
Cohesion: 0.29
Nodes (7): T1 终端设置（M，已完成：feat/T1-terminal-settings）, T2 终端内搜索（S，已完成：`feat/T2-terminal-search`）, T3 广播输入（M）, T4 会话日志（M）, T5 关键词高亮（M，方案：`docs/proposals/T5-highlight.md`，未实现）, T6 代码片段（M，实现：`feat/T6-snippets`，尚未合并）, T 终端

### Community 15 - "S 同步"
Cohesion: 0.33
Nodes (6): S1 WebDAV 同步（L）, S2 同步设置与状态（M）, S3 新设备加入（M）, S4 S3 兼容存储（M）, S5 同步文件夹（S）, S 同步

### Community 16 - "AGENTS.md"
Cohesion: 0.08
Nodes (22): 交付, 工作流程, 常用命令, 常见坑, 必读文档, 测试要求, 目录地图, 硬性约束 (+14 more)

### Community 17 - "totp.go"
Cohesion: 0.12
Nodes (16): Code(), CodeRaw(), codeRaw(), decodeSecret(), hashFor(), normalizeSecret(), Parse(), parseURL() (+8 more)

## Knowledge Gaps
- **2 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `app` connect `app` to `app.go`, `tab`, `client.go`, `数据存储与同步设计`, `Host`, `架构`, `.askChallenge`, `AGENTS.md`?**
  _High betweenness centrality (0.451) - this node is a cross-community bridge._
- **Are the 7 inferred relationships involving `app` (e.g. with `常见坑` and `硬性约束`) actually correct?**
  _`app` has 7 INFERRED edges - model-reasoned connections that need verification._
- **Should `app` be split into smaller, more focused modules?**
  _Cohesion score 0.05384150030248034 - nodes in this community are weakly interconnected._
- **Why does `开发计划` connect `开发计划` to `C 连接`, `F 基础`, `R 发布`, `P 文件（SFTP）`, `T 终端`, `S 同步`, `AGENTS.md`?**
  _High betweenness centrality (0.211) - this node is a cross-community bridge._
- **Should `app.go` be split into smaller, more focused modules?**
  _Cohesion score 0.07071887784921099 - nodes in this community are weakly interconnected._
- **Why does `Config` connect `client.go` to `C 连接`, `Host`?**
  _High betweenness centrality (0.179) - this node is a cross-community bridge._
- **Should `tab` be split into smaller, more focused modules?**
  _Cohesion score 0.06390977443609022 - nodes in this community are weakly interconnected._