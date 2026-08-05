---
name: 🌸 HER Hack-Astron 出题
about: 面向 Go 智能体引擎、WebSocket 协议、工具与权限系统发布赛题
title: 'HER Hack-Astron #出题｜赛题名称'
labels: ['HER Hack-Astron']
---

> **赛题确认：** 由 @FenjuFu 审核、分配期号并改名后，Issue 才成为正式赛题。
>
> **活动标签：** 模板自动添加 `HER Hack-Astron`。

## 命题背景

- 出题组织：{{组织名称}}
- 引擎问题：{{协议、推理循环、工具、权限、Provider、上下文或会话}}
- 目标负载：{{并发、消息规模、工具数量与运行环境}}

## HarnessClaw Engine 赛题方向

- WebSocket 会话握手与 `session.create → user.message → task.end` 流式事件契约
- 五阶段 Query Loop、并行 / 串行工具执行与错误恢复
- Bash / FileRead / FileEdit / Grep / Glob / WebFetch 等工具扩展
- 六阶段权限决策、只读自动授权与客户端审批
- Anthropic / OpenAI / Bedrock / Vertex 等 Provider 适配、重试与切换
- 上下文压缩、会话生命周期、并发 fan-out 和空闲回收
- `SKILL.md` 加载、参数替换和优先级覆盖

## 技术契约

- 影响包：{{internal/channel / engine / permission / provider / skill / tool / storage}}
- 协议变化：{{新增事件、字段及向后兼容}}
- 错误模型：{{错误码、重试性与客户端行为}}
- 性能目标：{{延迟、吞吐、内存或竞态}}

## 交付与验收

- 符合现有依赖方向的 Go 实现，不引入循环依赖
- 单元测试覆盖正常、取消、超时、重试和并发边界
- `make test`（含 race）通过；协议改动同步文档和客户端示例
- 新工具纳入注册、Schema、权限检查和审计链路
- 不在日志、Fixture 或配置中提交 API Key
- 提供基准或可复现的前后对比，说明兼容与回滚

## 提交与参与

- PR 标题：`[HER Hack-Astron #期号] 作品名称 + Engine 能力`
- PR 附设计、协议示例、测试命令、race / benchmark 结果
- 女性贡献者占实际代码记录 **≥ 50%**

## 评审重点

- 协议正确性、并发安全与错误语义
- 权限边界和工具执行安全
- Provider / 客户端兼容性
- 测试深度、性能证据和文档质量

出题 / 合作 / 发奖咨询：ifly_opensource@iflytek.com
