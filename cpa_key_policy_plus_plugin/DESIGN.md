---
name: CPA Key Policy+
description: Key policy, usage receipts and carpool shares for CPA native keys
colors:
  receipt-counter: "#e4e6e9"
  receipt-paper: "#fbfbfa"
  receipt-sunken: "#f0f1f1"
  thermal-ink: "#26282c"
  faded-ink: "#4a4e55"
  margin-ink: "#666a72"
  dashed-rule: "#bfc3c9"
  tail-pink: "#b8356a"
  stamp-red: "#b42f28"
  carbon-blue: "#2c54c2"
  carbon-counter: "#101113"
  carbon-paper: "#1b1d20"
  carbon-ink: "#e9eae6"
  carbon-faded-ink: "#c4c7cb"
  carbon-margin-ink: "#9ea2a9"
  carbon-rule: "#3c4046"
  carbon-tail-pink: "#f27daa"
  carbon-stamp-red: "#ff7c72"
  carbon-blue-light: "#8aa8ff"
  host-primary: "#409eff"
  host-primary-deep: "#2e72b8"
  host-surface: "#ffffff"
  host-text: "#2c3e50"
  host-text-secondary: "#5f6c7b"
  host-success: "#67c23a"
  host-warning: "#e6a23c"
  host-danger: "#f56c6c"
typography:
  headline:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Microsoft YaHei, Noto Sans CJK SC, Source Han Sans SC, system-ui, sans-serif"
    fontSize: "1.375rem"
    fontWeight: 700
    lineHeight: 1.3
  title:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Microsoft YaHei, Noto Sans CJK SC, Source Han Sans SC, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 700
    lineHeight: 1.35
  body:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Microsoft YaHei, Noto Sans CJK SC, Source Han Sans SC, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Microsoft YaHei, Noto Sans CJK SC, Source Han Sans SC, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 600
    lineHeight: 1.4
  figures:
    fontFamily: "KP Receipt, PingFang SC, system-ui, sans-serif"
    fontSize: "1.375rem"
    fontWeight: 700
    fontFeature: "\"tnum\", \"zero\""
rounded:
  slip: "3px"
  sm: "6px"
  md: "10px"
  host-sm: "8px"
  host-md: "12px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "22px"
  page: "24px"
components:
  receipt-button:
    backgroundColor: "{colors.receipt-paper}"
    textColor: "{colors.thermal-ink}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "34px"
  receipt-button-primary:
    backgroundColor: "{colors.thermal-ink}"
    textColor: "{colors.receipt-paper}"
    rounded: "{rounded.sm}"
    padding: "0 14px"
    height: "34px"
  receipt-input:
    backgroundColor: "{colors.receipt-paper}"
    textColor: "{colors.thermal-ink}"
    rounded: "{rounded.sm}"
    padding: "0 10px"
    height: "34px"
  receipt-slip:
    backgroundColor: "{colors.receipt-paper}"
    textColor: "{colors.thermal-ink}"
    rounded: "{rounded.slip}"
    padding: "22px"
  admin-button-primary:
    backgroundColor: "{colors.host-primary-deep}"
    textColor: "{colors.host-surface}"
    rounded: "{rounded.host-md}"
    padding: "0 14px"
    height: "34px"
  pool-block:
    backgroundColor: "{colors.host-surface}"
    textColor: "{colors.host-text}"
    rounded: "{rounded.host-md}"
    padding: "14px 16px"
---

# Design System: CPA Key Policy+

## Overview

**Creative North Star: "The Receipt and the Ledger"**

The plugin has two faces with one set of facts behind them. The key holder's page is a thermal receipt: every model call prints as a line that can be reconciled, the quota windows and the carpool share sit at the top of the slip like a balance, and the totals close under a double rule. The maintainer's page is not a world of its own at all; it is a ledger that lives inside CPAMP and takes the host's colours, radii and controls, so the plugin reads as part of the manager rather than a guest in an iframe.

Both faces answer "can this key be used right now" before anything else, and both print every state as words, never colour alone. Density is high but calm: figures align on tabular digits, rules separate sections, and there is almost no ornament. The receipt refuses torn edges, paper grain and stacked shadows; the ledger refuses a second design system on top of CPAMP.

Carpooling adds one block per upstream account on the ledger and one more window on the receipt, and the same slip, cut down, becomes the pinnable card.

**Key Characteristics:**
- Figures in tabular, slashed-zero digits; on the receipt they print in the "KP Receipt" face.
- One colour per state, and a state is always a marker or stamp plus words.
- Dashed rules between receipt sections, a double rule over totals.
- Admin surfaces inherit CPAMP tokens live; local values are only fallbacks.
- Times that bind (resets, windows) are shown in Beijing time and say so.

## Colors

Ink on paper for the receipt, CPAMP's own palette for the ledger, and exactly three state colours that each mean one thing.

### Primary
- **Thermal Ink** (#26282c): body text, figures, meter fills and the primary button on the receipt; on carbon copies it becomes Carbon Ink (#e9eae6).
- **Host Primary Deep** (#2e72b8): primary buttons inside CPAMP. It is CPAMP's primary (#409eff) mixed 72% with black, the least change that carries white text at 4.9:1.

### Neutral
- **Receipt Paper** (#fbfbfa) on **Counter Grey** (#e4e6e9): the slip and the counter it lies on. Carbon copy: Carbon Paper (#1b1d20) on Carbon Counter (#101113).
- **Faded Ink** (#4a4e55) and **Margin Ink** (#666a72): secondary text and timestamps; carbon values #c4c7cb and #9ea2a9.
- **Dashed Rule** (#bfc3c9): section rules on the slip; carbon #3c4046.
- **Host Surface / Host Text** (#ffffff / #2c3e50, secondary #5f6c7b): fallbacks only; inside CPAMP the injected variables win.

### Tertiary (state colours)
- **Tail Pink** (#b8356a, carbon #f27daa): close to a limit or a share running out.
- **Stamp Red** (#b42f28, carbon #ff7c72): exceeded, blocked, share used up.
- **Carbon Blue** (#2c54c2, carbon #8aa8ff): billing pending and a running burst ("爽蹬中").
- On the ledger the same three roles use CPAMP's warning, danger and primary colours, pulled toward the text colour for text (`color-mix` 45–55%) so labels stay readable in both themes.

### Named Rules
**The One Colour, One Meaning Rule.** Pink means near, red means blocked, blue means pending or burst. Nothing else on the page is coloured; everything else is ink.

**The Stamp Only When It Blocks Rule.** Red appears only when a request would be refused right now. A warning is never red.

## Typography

**Body Font:** the CJK system sans stack (PingFang SC, Hiragino Sans GB, Microsoft YaHei UI, Noto Sans CJK SC, then system-ui)
**Figures Font:** KP Receipt, an Iosevka subset (SIL OFL 1.1) inlined as base64, on the receipt only; the ledger keeps CPAMP's font.

**Character:** a plain CJK sans carries the language; a narrow monospaced figure face carries money, tokens, percentages and times, so columns align like a till roll.

### Hierarchy
- **Headline** (700, 1.375rem, 1.3): the receipt verdict ("现在可以使用", "份额已用完") and the admin page title (1.5rem).
- **Title** (700, 1rem–1.125rem, 1.35): section heads on the ledger (1.125rem) and pool names (1rem).
- **Body** (400, 0.875rem, 1.55): running text; detail lines use 0.8125rem.
- **Label** (600, 0.75rem): definition terms, table heads, window captions.
- **Figures** (700, 1.375rem window figures, 1.75rem totals, 1.875rem on the card): tabular with slashed zero.

### Named Rules
**The Aligned Figures Rule.** Every number that can be compared vertically uses tabular digits; money keeps two decimals, percentages of a share keep two, account percentages none.

## Layout

The receipt is a desk: a balance slip (300–372px) beside a roll of lines (up to 780px), centred in 1180px, 24px gap; below 920px they stack into one strip, and at 390px the gutter is 10–12px with no horizontal scroll. The balance slip is sticky and clamps itself so its bottom is never cut off.

The ledger is a single column up to 1480px: header with sync state, a facts row, the "需要处理" inbox, the carpool blocks, then the key table with inline editors and a sticky save bar. Below 860px tables turn into two-column label/value grids.

A carpool block reads top to bottom: account line and live state; a facts row (weekly used with meter, 100% ≈, 1% ≈, today's conversion, reset, 5-hour window when the plan has one); the week/month chart; the member table with a 7×24 heatmap per row that expands to a 30-day strip; the attribution footnote; burst controls; warnings.

## Elevation & Depth

The receipt uses one soft two-layer shadow for slips (`0 1px 1px` plus a long, low `0 16px 36px -22px`) so paper sits on the counter; nothing else on the slip is raised. The ledger is flat: depth comes from CPAMP's borders and tonal fills (sunken surfaces for the burst row, editors and empty states), never from shadows, except the save bar's upward fade.

### Named Rules
**The One Sheet Rule.** A slip is the only raised thing on the receipt, and nothing inside a ledger block gets its own border; inner regions use a tonal fill instead.

## Shapes

Slips have barely-cut corners (3px). Controls use 6px on the receipt and CPAMP's 8px/12px on the ledger. Meters are 4–6px pills whose fill is revealed with `clip-path`, so updates animate without layout. Heatmap cells are 7px squares with 1px gaps (day strip cells are square and fluid). Status markers are 8px rounded squares; stamps are 1.5px outlined labels.

## Components

### Buttons
- **Shape:** 34px high, 6px radius (receipt) or CPAMP's radius (ledger); small buttons 28px.
- **Primary:** ink fill with paper text on the receipt; Host Primary Deep with white text on the ledger.
- **Hover / Focus:** hover darkens the fill or shifts the border; focus is a 2px outline in ink (receipt) or the host accent (ledger). On light CPAMP themes hovered text uses Host Primary Deep instead of CPAMP's 2.5:1 blue.
- **Busy / Done:** a spinner prefix while busy, a brief success border when done; destructive actions arm first ("确认…？") and disarm after 4 seconds.

### Inputs / Fields
- **Style:** 34px, 1px strong line border, surface fill; placeholders in secondary text at full opacity.
- **Focus:** border in ink plus a 3px selection-colour ring.

### Receipt window (signature)
A caption with a stamp on the right (正常 / 接近上限 / 已超限 as words), figures "used / limit" with "剩" right-aligned, a 5px meter, and a footnote with the reset or recovery time. The carpool share is printed as one more window: share used / available, remaining pp and ≈ USD, account week and reset, 1% ≈, then a dotted team list whose names follow the pool's visibility.

### Card slip (signature)
The receipt cut down to what matters at a glance: key name and live dot, one main block (whatever blocks the key first, otherwise the share or the primary window as a big "剩 …" figure with meter), the other limited windows as rows, and a double-ruled footer with the sync time and a refresh button. It is the same slip in the page (`?view=card`) and in the Picture-in-Picture window.

### Pool block (signature, ledger)
Bordered once, CPAMP surface, one per upstream account. The member table prints role, share (with carry), used (with cost and pending), remaining (with ≈ USD or overshoot), state as marker plus words, and a 7×24 heatmap coloured in four quartile steps of the host accent.

### Chart
Pixel-drawn SVG measured to its container: week view plots the running capacity estimate with an uncertainty band over the whole cycle and a dashed "now" line; month view plots daily closes with hollow dots for single-day conversions. Pointer and arrow keys scrub; the reading is printed below the chart, not in a floating tooltip.

## Do's and Don'ts

### Do:
- **Do** print every state as words next to its colour (stamps, marker plus text).
- **Do** keep figures tabular and right-aligned where they are compared.
- **Do** show reset and window times in Beijing time and label them "北京时间".
- **Do** take colours, radii and controls from CPAMP's injected variables on the ledger, and scope overrides under `#kp` only where the host breaks legibility.
- **Do** lead with whatever blocks the key (billing hold, used-up window, used-up share) before anything else.

### Don't:
- **Don't** add torn edges, paper grain or stacked shadows to the receipt.
- **Don't** use red for anything that does not block a request.
- **Don't** give ledger elements class names containing "card" or "panel"; CPAMP repaints them.
- **Don't** nest bordered boxes inside a pool block.
- **Don't** put a kicker or eyebrow label above a heading.
