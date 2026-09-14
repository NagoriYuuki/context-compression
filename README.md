# Context Compression Middleware

这是一个独立的 Go 上下文压缩中间件示例，接收已经规范化的消息数组，在调用 LLM 前检查输入预算，并在超限时压缩旧历史。

## 运行

```bash
go test ./...
go test -race ./...
go vet ./...
```

项目不调用真实 LLM。默认 `RuneBudgetCounter` 只用于可复现的离线演示；实际接入时应注入目标模型对应的 TokenCounter。

## 设计

压缩顺序固定为：

1. 清理已完成 ToolRound 中的旧 Tool Result，保留调用关系和元数据；
2. 最多调用一次可替换的 `Summarizer`，摘要旧的、未受保护的历史 Unit；
3. 摘要失败、超时、为空或变大时，执行确定性的文本截断、结构化占位或低价值日志删除；
4. 重建消息并校验预算、顺序和 Tool 关系。

System/Developer、最新 User、未完成 ToolRound 和 Tool 元数据不会被语义摘要或 fallback 修改。输入会先深拷贝，无法安全满足预算时返回 `CannotFit`；输入协议非法时返回 `InvalidInput`。

## 验证

`fixtures/long_session.json` 覆盖目标、约束、两轮 Tool 交互、失败原因、关键决策、待办和最新请求。`fixture_test.go` 验证压缩后关键消息仍存在、最新请求保持原文，且输出仍满足结构校验。

## 当前限制

- 默认 Token 估算不等于供应商真实 Token 数；
- 不做自然语言任务状态抽取；
- 不修改或语义摘要 System/Developer；
- 不接长期记忆、向量检索和真实摘要服务。

实际投入时间：请在最终提交前根据真实情况补充。
