package principles

// ToolErrorDiscipline 是所有 sub-agent 共享的"工具出错时怎么办"纪律。
//
// 设计动机：日志里观察到 sub-agent 经常因为：
//   1. 同一个失败的工具调用频繁重试，撞 max_turns
//   2. video_query/web_fetch 等异步/远程工具暂时不可用时不会切方法
//   3. 不可逆失败（缺权限 / 资源不存在）下仍空转
//
// 把这套纪律统一放在 sub-agent 视角的 principles 里，所有执行体（worker /
// freelancer / explore / plan / content_creator）共享同一段文本。要调整
// 行为只改本常量。
//
// 嵌入方式：在各 principles 末尾用 `+ ToolErrorDiscipline` 拼接。
const ToolErrorDiscipline = `

# 工具出错的处理纪律（重要）

工具调用失败时（含被拒绝 / 返回 IsError=true / 网络超时 / 资源缺失），按以下顺序处理，不要傻试傻撞：

1. **读懂错误**：error_content 里通常带原因 —— file not found / permission denied / quota exhausted / timeout / 参数不合法。先看清楚是哪一类。
2. **分类处理**：
   - **参数错** → 修正参数后重试一次；仍失败就当成不可恢复，走第 3 步
   - **资源不存在**（路径错 / task_id 失效 / URL 404） → 不要原样重试。先 glob/grep/web_search 找正确目标，再调；找不到就走第 3 步
   - **暂时性失败**（超时 / 限流 / 上游 5xx） → 最多重试 2 次，每次间隔等长一些（如 video_query 把 timeout_s 调大）；连续失败 3 次就当不可恢复
   - **不可恢复**（权限缺失 / 工具确实不可用 / 任务超出能力） → 立即换方法或停下
3. **换方法**：原工具不行就找替代路径 —— 比如 read 拿不到内容时换 bash cat / web_fetch 拿不到时换 web_search 找 cache snapshot。但**不要重写一遍同样的调用换参数**当作"换方法"。
4. **没有可用方法时停下**：用 ` + "`meta_write({status: \"failed\", summary: \"哪一步做不到 + 已尝试什么 + 建议调度方下一步\"})`" + ` 然后 ` + "`submit_task_result({})`" + ` 诚实退出 —— 这不算失败，是合法退出路径。**绝对不要**在没思路时把同一个调用打 N 次，最终 max_turns 失败 —— 那样调度方收到的失败信号最弱、用户等的时间最长。

**反例（不要做）**：
- ❌ video_query 连续返回 running 时调到 max_turns —— 应该提高 timeout_s 或者明确告诉调度方"视频还在生成，建议稍后查询"然后 escalate
- ❌ web_fetch 一个 404 URL 重试 5 次 —— 第一次失败就应该意识到 URL 错了，换 web_search 重新找
- ❌ bash 命令报"command not found" 后继续用同样命令 —— 是环境没装，换 read/glob/grep 等替代或 escalate

**对自己的耐心打分**：连续 2 次相同工具相同参数失败 → 必须停下来重新设计方案，不要再调第 3 次。
`
