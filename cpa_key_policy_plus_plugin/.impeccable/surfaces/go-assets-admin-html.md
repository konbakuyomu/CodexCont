---
version: 1
slug: "go-assets-admin-html"
primary_target: "go/assets/admin.html"
related_targets: []
---

Scope: `go/assets/admin.html` — CPAMP 插件菜单页「CPA Key Policy+」，被 CPAMP 以 iframe 嵌入，Operate 模式。
Audience/job: 维护者本人；快速发现缺策略、接近上限、被冻结的 Key 并就地处理。
Constraints: 继承 CPAMP 宿主主题（注入变量、data-theme、!important 控件样式）；保留测试锁定的文案与 API（/key-policy-plus/api、套用 CPA 发现模型、is-busy/is-done 等）；不出现 Plus 侧 Key 创建/删除/复制、并发、Codex 窗口。

## Direction contract

THESIS: 管理页先回答「现在有什么要处理」，再给出全部 Key 的紧凑总览；编辑在表格行里就地展开，所有改动汇总到底部保存栏。拒绝左右分栏的大详情面板和弹窗式编辑。

OWN-WORLD: 完全继承 CPAMP：颜色、圆角、按钮、输入框、表格取自 CPAMP 注入的变量（--bg-primary、--text-primary/secondary/tertiary、--border-color、--primary-color、--success/--warning/--danger-color、--app-radius-sm/md），跟随 data-theme 亮暗；单独打开时回落到同值的本地变量。状态色只出现在行首 3px 细边和文字标签上，表格本体保持中性。数字等宽对齐。

STORY: 维护者进来先看「需要处理」：待核算冻结、缺 RPM 或额度、接近或超过上限，每条带一个直接动作；再扫一遍全部 Key 的用量；点开一行改启用、RPM、限额、模型白名单或做软重置；底部保存栏写明改了几处，保存或放弃。

FIRST VIEWPORT: 顶部一行是标题「CPA Key Policy+」、价格源与同步状态、刷新。其下是「需要处理」列表（没有事项时收成一行「全部 Key 策略完整」）。再下是全部 Key 表格：名称与预览、状态、RPM、5H/24H/7D/月（已用百分比加细条）、模型数，搜索框在表格标题右侧。行展开即编辑区。底部吸附的保存栏只在有改动时出现。

FORM: 待处理收件箱 + Key 表格，我的结构排序第 3 位；surface seed c85f11c1。加强：输入新限额时即时显示按当前用量换算的百分比（来自暗房试样条）；状态色只落在细边和文字上（来自云边虹彩）。模型白名单在展开行内编辑，不再用弹窗。

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Extension: carpool pools (2026-09-27)

One block per upstream account sits between the inbox and the key table: account line and live state, a facts row, the week/month chart, the member table with a 7×24 heatmap per row (click for 30 days), the attribution footnote, burst controls and warnings. Pool warnings and used-up shares join the inbox. Status is a marker plus words; inner regions use tonal fills, never a second border. Charts and heatmaps run on Beijing time and say so. The "3px 细边" line above is read as marker plus text, per the craft floor.
