---
version: 1
slug: "go-assets-user-html"
primary_target: "go/assets/user.html"
related_targets: []
---

Scope: `go/assets/user.html` — CPA 用量自助页（cpa-usage.konbakuyomu.us），Operate 模式。
Audience/job: 拿到分发 Key 的朋友和同事，电脑和手机都用；要回答「还能不能用、还剩多少、为什么被拦、钱花在哪」。
Constraints: 自包含（内联 CSS/JS，无外部字体/CDN）；前端不重算额度与价格；保留测试锁定的文案与固定主范围逻辑（weekly_only → 7d，否则 24h 为主范围）；不出现 CodexCont/思维链保护。

## Direction contract

THESIS: 用量页是这把 Key 的流水小票。每次模型调用打印成一行可对账的消费，展开就是「Token × 单价 = 金额」的算式；四个额度窗口是小票顶部的余额栏。拒绝行业默认的深色 KPI 卡片网格加宽表格。

OWN-WORLD: 纸白 #FBFBFA 的纸条放在柜台灰 #E6E8EB 上；热敏墨 #2B2D31 印正文，褪色墨 #7B7F87 印次要信息；金额、Token、时间一律等宽数字、按小数点对齐；虚线分隔，合计放大加粗。状态色一色一义：卷尾粉 #D6457C = 接近上限，印章红 #C8322B = 已超限或被拦截，复写蓝 #2F5BD3 = 待核算，其余全部墨色。夜间是复写联：#17191C 底、#E7E8E4 字。不做撕边、纸纹和层层阴影。

STORY: 持有人先看到能不能用（余额单顶部状态和四个窗口剩余），再看近 24 小时逐小时怎么花的，最后在消费明细里找到任意一笔、展开核对算式；被拦时看得到是哪个窗口、差多少、何时回落。

FIRST VIEWPORT: 桌面是柜台上左右两张纸条：左侧约 380px 的余额单（标题、Key 名与预览、实时状态、四个窗口的已用/上限/剩余和墨条、24 小时柱状图、调用统计），随页面吸顶；右侧消费明细纸条占满剩余宽度，顶部是「全部 / 只看失败 / 模型」筛选，按天分组逐行打印。手机是余额单在上、明细在下的一条纸带。刷新和退出是余额单页眉里的小号墨色按钮。

FORM: 小票账本（thermal receipt），我的方向排序第 6 位；seed 5f257e4f。签名交互：新请求像小票一样被「打印」进明细顶部（短促下推并显影，约 180ms；reduced-motion 下直接出现）。加强：金额按小数点固定位对齐（来自辉光管计数器）；状态色一色一义（来自街机屏）；只有拦截才用印章红（来自 Kraftwerk）；时间戳固定在左侧留白栏（来自折纸）；每个状态都印出文字、不只靠颜色（来自天幕灯光）。

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Extension: carpool share and card (2026-09-27)

The carpool share prints as one more window on the balance slip, between the verdict and the quota windows: share used / available, remaining pp and ≈ USD, the account's week and reset, 1% ≈ $, and a dotted team list whose labels follow the pool's visibility (匿名 / 实名 / 只看自己). The verdict treats a used-up share like a used-up window ("份额已用完"). `?view=card` is the same slip cut down to one main block, the other limited windows and a double-ruled footer; "钉到桌面" opens that card in a Picture-in-Picture window (popup fallback). No new colours: the share uses the same stamps and meters.
