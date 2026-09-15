# Context Compression Middleware

这是一个独立的 Go 上下文压缩中间件示例，接收已经规范化的消息数组，在调用 LLM 前检查输入预算，并在超限时压缩旧历史。

## 运行

使用 Go 1.27.1（与 `go.mod` 一致），在项目目录执行：

```bash
make verify   # 构建、vet、全量测试、竞态检查
make demo     # 打印五个场景的压缩前后全文
```

`make help` 列出全部目标。不使用 make 时，等价命令为：

```bash
go build ./...
go vet ./...
go test -count=1 ./...
go test -race ./...
go test -count=1 -run '^TestCompressionDemo$' -v
```

项目不调用真实 LLM。默认 `RuneBudgetCounter` 只用于可复现的离线演示；实际接入时应注入目标模型对应的 TokenCounter。

## 设计

压缩顺序固定为：

1. 清理已完成 ToolRound 中的旧 Tool Result，保留调用关系和元数据；
2. 最多调用一次可替换的 `Summarizer`，摘要旧的、未受保护的历史 Unit；
3. 摘要失败、超时、为空、变大，或摘要后仍超预算时，执行确定性的文本截断、结构化占位或低价值日志删除；
4. 重建消息并校验预算、顺序和 Tool 关系。

摘要调用受 `Middleware.SummarizeTimeout` 限制，`NewMiddleware` 默认设为 `DefaultSummarizeTimeout`（30s）。该时限由调用方 Context 派生，因此调用方取消仍然优先生效；设为 0 表示不设内部时限，完全交给调用方。选择 30s 的依据是：摘要是真实模型调用前的同步前置步骤，而它的失败路径是确定性 fallback，等待超过一次典型模型调用的时间，不如尽早降级。交互式场景可以调小，离线批处理可以调大。`Summarizer` 实现必须尊重传入的 Context。

System/Developer、最新 User、未完成 ToolRound 和最近一次已完成 ToolRound 不参与清理、语义摘要或 fallback；最近一次已完成结果被保留，是因为结果已返回不代表模型已经消费它。保留的 Tool 调用元数据保持原文，整轮摘要时调用和结果一起替换。输入会先深拷贝，无法安全满足预算时返回 `CannotFit`；输入协议非法时返回 `InvalidInput`。

## 直观看压缩前后全文

直接打开 [DEMO.md](DEMO.md)。这是一份实际运行生成的对照文档，只包含简短结果和两个部分：

- **压缩前：完整原文**，15 条消息，包括全部日志、工具调用参数和工具结果。
- **压缩后：完整内容**，12 条消息，包括实际生成的摘要及保留的所有原文。

正文按真实顺序展开，没有预览截断，也不重复列出 Actions、诊断和检查表。本例输入预算为 900，估算 Token 从 3924 降到 814。摘要由离线 Fake 提供。

重新运行并在终端查看全部五个场景：

```bash
make demo
```

五个场景分别是 `fit`、`clear_tool_result`、`summary`、`fallback` 和 `cannot_fit`，对应不压缩、工具结果清理、历史摘要、摘要失败降级和必要内容超预算。每个场景都带断言，实际走到的 Action 序列与这里的描述不一致时测试会失败。只看其中一个场景时追加子测试名：

```bash
go test -count=1 -run '^TestCompressionDemo$/^summary$' -v
```

需要完整消息元数据、Actions 和诊断时，或需要在场景检查通过后重新生成 `DEMO.md` 时：

```bash
make demo-full
make demo-update
```

## 验证说明

`fixtures/long_session.json` 是虚构的订单 API 超时排查会话，包含目标、约束、三轮工具交互、权限失败、事实、决策和待办。工具参数中的路径仅为历史记录，运行时不会打开对应文件。

演示默认隐藏检查过程，但仍断言实际路径、预算、摘要调用次数和来源。`fixture_test.go` 独立于中间件的 `validateOutput` 检查关键消息全文、工具配对及参数、顺序、JSON 内容类型和调用方输入不变。

关键任务信息通过调用方显式的 `Protected` 和标签保护。样例证明这些信息在输出中仍然可读取，不代表真实 Agent 已继续完成任务，也不证明任意摘要的语义等价性。

## 当前限制

- 默认 Token 估算不等于供应商真实 Token 数；
- 不做自然语言任务状态抽取；
- 内部超时只能放弃等待，无法强制中断不尊重 Context 的 `Summarizer` 实现；
- 摘要只作用于最早一段连续的可压缩历史，不迭代后续片段；
- 每次修改后重新全量计数，接入高成本 Tokenizer 时需要自行加缓存；
- 演示覆盖单次压缩，未验证多轮摘要累积的来源传播和信息衰减；
- 不修改或语义摘要 System/Developer；
- 不接长期记忆、向量检索和真实摘要服务。

实际投入时间：请在最终提交前根据真实情况补充。
