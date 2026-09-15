# Context Compression Middleware

这是一个独立的 Go 上下文压缩中间件示例，接收已经规范化的消息数组，在调用 LLM 前检查输入预算，并在超限时压缩旧历史。

## 运行

使用 Go 1.27.1（与 `go.mod` 一致），在项目目录执行：

```bash
go build ./...
go test -count=1 ./...
go test -race ./...
go vet ./...
```

项目不调用真实 LLM。默认 `RuneBudgetCounter` 只用于可复现的离线演示；实际接入时应注入目标模型对应的 TokenCounter。

## 设计

压缩顺序固定为：

1. 清理已完成 ToolRound 中的旧 Tool Result，保留调用关系和元数据；
2. 最多调用一次可替换的 `Summarizer`，摘要旧的、未受保护的历史 Unit；
3. 摘要失败、超时、为空、变大，或摘要后仍超预算时，执行确定性的文本截断、结构化占位或低价值日志删除；
4. 重建消息并校验预算、顺序和 Tool 关系。

System/Developer、最新 User 和未完成 ToolRound 不参与语义摘要或 fallback；保留的 Tool 调用元数据保持原文，整轮摘要时调用和结果一起替换。输入会先深拷贝，无法安全满足预算时返回 `CannotFit`；输入协议非法时返回 `InvalidInput`。

## 验证

### 直接看功能演示

```bash
go test -count=1 -run '^TestCompressionDemo$' -v
```

`-v` 显示演示报告，`-count=1` 确保本次重新执行。仅看到普通 `go test` 的 `ok` 不会展示压缩过程。

五个场景共用 `fixtures/long_session.json` 中的订单 API 超时排查任务。样例包含 15 条消息、三轮 Tool 交互、目标、只读约束、权限失败、关键决策、连接池配置证据和待办。日志与工具调用记录均为虚构数据；工具参数中的路径只表示历史记录，运行时不会打开这些文件。

以下是当前 fixture 和默认计数器的实际运行结果，数字均为估算值：

| 子场景 | 输入预算 | 压缩前 → 后 | 摘要器调用次数 | 可观察结果 |
| --- | ---: | ---: | ---: | --- |
| `fit` | 4500 | 3924 → 3924 | 0 | `fit`，消息与元数据原样返回 |
| `clear_tool_result` | 1700 | 3924 → 1612 | 0 | `degraded`，大日志换成占位文本，调用和结果仍成对 |
| `summary` | 900 | 3924 → 814 | 1 | `degraded`，清理后再用一条有来源的摘要替换旧历史 |
| `fallback` | 900 | 3924 → 900 | 1 | `degraded`，Fake 报错后截断旧文本，不重试摘要 |
| `cannot_fit` | 300 | 690 → 690 | 0 | `cannot_fit`，必要内容保持原文，说明仍超预算 |

每个场景额外预留 200 的输出空间和 100 的安全余量；例如 `summary` 的输入预算为 `1200 - 200 - 100 = 900`。`cannot_fit` 使用同一会话去掉四条过程性历史后的 11 条必要消息，保留重要工具结果所需的完整调用关系。

报告依次给出预算和触发原因、实际摘要调用次数、Actions、实际输出顺序、逐消息前后对照、替换文本全文、摘要角色与 `SourceIDs`、工具配对及参数、关键任务信息全文、Diagnostics 和失败原因。

建议先单独看摘要场景：

```bash
go test -count=1 -run '^TestCompressionDemo$/^summary$' -v
```

重点检查这三处：

1. `clear_tool_result` 后还有 `summarize`，且摘要器实际调用次数为 1。
2. 新消息 `summary:u-history` 的角色为 `assistant`、`Synthetic=true`，`SourceIDs` 为 `u-history`、`a-logs`、`r-logs`、`a-logs-followup`；旧调用与结果一起被替换，摘要插在原历史位置。
3. “任务继续性证据”里能读到原文：禁止修改生产数据、查询缺少 SELECT 权限、改查离线日志、连接池上限为 8、下一步核对并发等信息均保留。

将命令里的 `summary` 换成表中的其他子场景名即可单独运行。需要逐字段检查完整输入输出时：

```bash
COMPRESSION_DEMO_FULL=1 go test -count=1 -run '^TestCompressionDemo$/^summary$' -v
```

也可以保存一次实际演示输出，供 review 时查看：

```bash
go test -count=1 -run '^TestCompressionDemo$' -v > /tmp/context-compression-demo.txt
```

### 演示与自动化验收的关系

`demo_test.go` 负责可读展示，并检查实际到达声明的路径、返回状态、预算、摘要调用次数及来源。演示路径不正确时测试会失败。

`fixture_test.go` 验证关键消息的完整内容与元数据、Tool Call/Result 配对及参数、消息顺序、摘要来源、JSON 内容类型和调用方输入不变；这些检查不复用中间件自身的 `validateOutput`。其余测试负责预算、消息模型、协议及失败边界等验证。

任务继续性依赖调用方显式提供 `Protected` 或 `goal/constraint/fact/decision/failure/pending` 标签。演示证明本样例所需信息仍可读取；Fake 摘要由人工根据样例编写，不代表自动语义理解、摘要语义等价，或真实 Agent 已继续完成任务。这五个场景是功能讲解入口，不等于穷尽所有验收输入。

## 当前限制

- 默认 Token 估算不等于供应商真实 Token 数；
- 不做自然语言任务状态抽取；
- 最近的已完成 Tool Result 没有独立保护窗口，需要保留的当前工具结果应显式标记 `Protected`；
- 演示覆盖单次压缩，未验证多轮摘要累积的来源传播和信息衰减；
- 不修改或语义摘要 System/Developer；
- 不接长期记忆、向量检索和真实摘要服务。

实际投入时间：请在最终提交前根据真实情况补充。
