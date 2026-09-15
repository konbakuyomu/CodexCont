# 本项目的工程决策记录

本项目启用独立的 `write-notes-like-deepseek` Skill。这里只保存工程决定及真实取舍；工具不依赖任务框架，不同步任务状态。

## 日常使用

1. 修改重要行为或选择方案前，按模块名、机制名检索现行及被否决记录，排除 `archived/`。已有归属就更新原文。
2. 当前接口与约束留在模块规范；具体需求与执行步骤留在任务资料；本目录记录为什么选它、为什么放弃其他真实选项、代价和验证边界。
3. 新提案放 `proposed/`；确实落地并核对代码/规则后才转 `implemented/`。决定改变时另写新篇并互链；任务结束不改变笔记状态。
4. 不编造备选方案、首次提出日期或验收结论。无法核实的旧资料留作历史，不强行转换为笔记。
5. 机械小改无需新建Note；修改涉及旧决定时同步对应事实和引用。文件搬迁前查入站引用，修改后复核当前文档链接。

## 格式与归属

沿用Skill的四种生命周期及六种分类：`{lifecycle}/{class}/yyyy-mm-dd-topic.md`。中文正文；`# Agent Note:`、`Status:`和标准章节按模板填写。不建立INDEX.md。用到哪个目录就建哪个，不预建空分类树。

归档只收已落地且不再约束当前工作的记录；归档后正文永久冻结。不能因年龄、任务归档或缺少新的更新就判它过时。关键理由应在笔记自身完整，历史任务链接只作补充证据。

若仓库里还有 `.codestable/`，把它当历史资料，不根据其中的旧状态启动新流程。

## 独立校验

在目标仓库根目录运行；脚本来自独立Skill目录，项目无需package.json。Windows默认安装示例：

```powershell
$notesHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $env:USERPROFILE '.codex' }
$notesSkill = Join-Path $notesHome 'skills/write-notes-like-deepseek'
node "$notesSkill/scripts/verify-agent-note-tree.ts"
node "$notesSkill/scripts/verify-agent-note-format.ts"
node "$notesSkill/scripts/verify-archived-agent-notes.ts"
```

仓库若同时维护 `.agents/notes` 与 `.trellis/spec`，可再跑：

```powershell
node "$notesSkill/scripts/verify-local-links.mjs" .agents/notes .trellis/spec --exclude .agents/notes/archived
```

校验结果需报告实际数量和退出码，0篇通过不能充当启用验收。格式正确不证明结论正确，仍需核对代码、约束和真实测试。

AI收尾必须执行相关校验和代码测试，并明确未验收项目；本项目不安装Git提交钩子或CI。
