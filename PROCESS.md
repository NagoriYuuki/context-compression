# PROCESS

## 工具和分工

本次实现使用 Codex 进行方案 review、代码实现、测试和本地验证。用户负责提出需求、审阅方案和分阶段 commit，并由用户决定何时 push。实现过程中没有调用真实 LLM 或外部摘要服务。

## 关键决策记录

初始方案包含任务状态提取、保护等级、近期 Token 比例、多个压缩阶段和 SystemReducer。review 后删除这些职责，收敛为消息协议校验、ToolRound 分组、TokenCounter、一次摘要和确定性 fallback。原因是作业核心要求是预算、Tool 一致性、失败路径和可验证性，复杂策略会增加实现面并引入无法证明的语义判断。

代表性判断：不使用关键词自动提取 goal、failure、pending，因为 Tool Result 中的关键词可能造成误判；改为使用调用方显式的 `Protected/Tags`，并通过 fixture 验证关键消息仍存在。

## 实现检查点

- 方案：已完成精简并提交；
- 数据模型和深拷贝：已完成并有单元测试；
- TokenCounter：已完成并有边界测试；
- 输入校验和 ToolRound：已完成并覆盖孤立 Result、重复 ID 和未完成轮次；
- 摘要接口和 FakeSummarizer：已完成并覆盖失败模式；
- Middleware 主流程：已完成并覆盖清理、摘要、fallback、CannotFit；
- 长会话 fixture 和文档：本次提交完成；
- 验证状态：`go test ./...`、`go test -race ./...`、`go vet ./...` 已通过。

## AI 建议的修改和验证

曾发现摘要失败 fallback 使用固定长度后，在预算极小时仍然可能超限。实现改为根据当前总 Token 预算再次选择可接受的头尾长度，必要时使用确定性占位内容；随后新增摘要失败测试并通过。

本次没有发生会话切换、上下文恢复或人工修改提交内容的情况。实际投入时间和最终人工 review 结果由提交者在最终提交前补充。
