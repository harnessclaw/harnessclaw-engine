package bash

// getDescription returns the LLM-facing prompt text for the Bash tool.
func getDescription() string {
	return `执行一条 bash 命令并返回输出。

工作目录在多次调用之间保持，但 shell 状态（如 export 的环境变量）不会保持。shell 环境从用户的 profile（bash 或 zsh）初始化。

重要：除非有明确指示或你已确认专用工具不行，否则不要用本工具跑 ` + "`find`" + `、` + "`grep`" + `、` + "`cat`" + `、` + "`head`" + `、` + "`tail`" + `、` + "`sed`" + `、` + "`awk`" + `、` + "`echo`" + ` 这类命令。请改用专用工具——它们交互更友好、权限审核也更直观：

 - 找文件：用 Glob（不要 find / ls）
 - 找内容：用 Grep（不要 grep / rg）
 - 读文件：用 FileRead（不要 cat / head / tail）
 - 改文件：用 FileEdit（不要 sed / awk）
 - 写文件：用 FileWrite（不要 echo > / cat <<EOF）
 - 输出文本：直接 assistant 输出（不要 echo / printf）

虽然 Bash 也能干同样的事，但内置工具体验更好，也方便审核与授权。

# 使用规范
 - 命令会创建新目录或文件时，先用本工具跑 ` + "`ls`" + ` 确认父目录存在且正确。
 - 路径含空格时用双引号包起来（例：cd "path with spaces/file.txt"）。
 - 整个会话尽量保持当前目录稳定——用绝对路径，少用 ` + "`cd`" + `。用户明确要求时才 ` + "`cd`" + `。
 - 可选 timeout（毫秒，最长 600000 = 10 分钟）。默认 120000（2 分钟）。
 - 写清楚 description 说明这条命令做什么。
 - 多条命令的并行/串行：
   - 互相独立可并行 → 同一条消息里发多次 Bash 调用。
   - 互相依赖必须串行 → 单次 Bash 调用用 '&&' 串起来。
   - 用 ';' 仅当串行执行但不在乎前面失败时。
   - 不要用换行分隔命令（引号字符串里的换行无碍）。
 - git 命令规范：
   - 优先新建 commit，而非 amend。
   - 跑破坏性操作（git reset --hard、git push --force、git checkout -- 等）前，先想想有没有更安全的替代方案；只有真没办法时才用。
   - 用户没明确要求时，不要跳过 hooks（--no-verify）或绕过签名（--no-gpg-sign / -c commit.gpgsign=false）。hook 失败要去查根因。
 - 避免无谓的 ` + "`sleep`" + `：
   - 能立刻跑的命令之间不要 sleep——直接跑。
   - 不要在 sleep 循环里重试失败命令——去查根因。`
}
