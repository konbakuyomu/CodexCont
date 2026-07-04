# CPA Usage 四窗口额度展示修复

## Goal

让 `https://cpa-usage.konbakuyomu.us/` 的额度展示对小白用户清楚可读：不再把四个额度窗口压成 `24H / 7D`、`5H / 本月` 两张组合卡，而是明确展示 `5 小时额度`、`24 小时额度`、`7 天额度`、`本月额度` 四个独立维度。

## Requirements

- 用户页顶部继续保留核心监控卡：当前 Key、最近 24 小时费用、调用统计、Token、思考量、缓存活动。
- 用户页删除 `24H / 7D`、`5H / 本月` 两张组合额度卡。
- 用户页新增独立额度窗口区域，四张卡分别显示：
  - `5 小时额度`：最近 5 小时已用、上限、剩余。
  - `24 小时额度`：最近 24 小时已用、上限、剩余。
  - `7 天额度`：最近滚动 7 天已用、上限、剩余。
  - `本月额度`：北京时间本月 1 日至今已用、上限、剩余。
- 每张额度卡应显示轻量状态：`未设置上限`、`正常`、`接近上限`、`已超限`。
- 有上限时显示进度条；无上限时不伪造百分比。
- 文案使用中文全称，不再使用混合缩写作为用户可见标题。
- 不改变后端 API、额度计算逻辑、主监控 `range=24h` 固定窗口或管理员页。

## Acceptance Criteria

- [x] 用户页 HTML 不再包含 `24H / 7D`、`5H / 本月`。
- [x] 用户页 HTML 包含 `5 小时额度`、`24 小时额度`、`7 天额度`、`本月额度`。
- [x] 用户页仍固定请求 `/usage?range=24h` 和 `/events?range=24h&limit=100`，不恢复 range 下拉框。
- [x] 窄屏布局不会截断额度窗口文字。
- [x] `go test ./... -count=1 -timeout=120s` 在 `cpa_key_policy_plus_plugin/go` 通过。
- [x] `git diff --check` 通过。

## Notes

- Confirmed evidence: `cpa_key_policy_plus_plugin/go/assets/user.html` currently renders the two confusing cards at `24H / 7D` and `5H / 本月`, while the backend already exposes all four windows through `state.me.usage` and `state.me.limits`.
