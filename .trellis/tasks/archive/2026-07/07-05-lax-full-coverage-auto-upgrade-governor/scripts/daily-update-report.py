#!/usr/bin/env python3
import argparse
import datetime as dt
import html
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Tuple


LOG_DEFAULT = "/var/log/daily-update-report.log"
STATE_PATH = Path("/var/lib/lax-auto-upgrade-governor/state.json")
TIMEOUT = 18
GB = 1024 ** 3
FLOATING_TAGS = {"latest", "main", "master", "edge", "dev", "stable", "alpine"}
FIXED_TAG_RE = re.compile(r"^(?:v?\d+(?:\.\d+){0,3}(?:[-_A-Za-z0-9.]+)?|\d+\.\d+(?:[-_A-Za-z0-9.]+)?)$")
CATEGORY_LABELS = {
    "auto-upgrade": "🚀 自动升级",
    "notify-only": "🔔 只提醒",
    "manual-only": "🔒 不自动升级",
    "host-adapter": "🛠️ 自动适配",
    "unknown": "未分类",
}


@dataclass
class CheckResult:
    name: str
    kind: str
    status: str
    current: str = ""
    latest: str = ""
    category: str = ""
    note: str = ""


def run(cmd: List[str], timeout: int = TIMEOUT) -> Tuple[int, str, str]:
    try:
        proc = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout, check=False)
        return proc.returncode, proc.stdout.strip(), proc.stderr.strip()
    except subprocess.TimeoutExpired as exc:
        out = exc.stdout if isinstance(exc.stdout, str) else ""
        return 124, out.strip(), "timeout"
    except Exception as exc:
        return 1, "", str(exc)


def safe(value: Any, limit: int = 180) -> str:
    text = "" if value is None else str(value)
    text = re.sub(r"(?i)(token|password|passwd|secret|private[_-]?key|authorization|bearer)=\S+", r"\1=<redacted>", text)
    text = re.sub(r"https?://[^\s]+(token|key|secret|auth|password)[^\s]*", "<redacted-url>", text, flags=re.I)
    return text.replace("\n", " ").replace("\r", " ")[:limit]


def load_adapter_states() -> Dict[str, Dict[str, Any]]:
    if not STATE_PATH.exists():
        return {}
    try:
        data = json.loads(STATE_PATH.read_text(encoding="utf-8"))
    except Exception:
        return {}
    adapters = data.get("adapters") if isinstance(data, dict) else {}
    return adapters if isinstance(adapters, dict) else {}


def human_bytes(value: Any) -> str:
    try:
        number = int(value)
    except Exception:
        return "n/a"
    if number >= GB:
        return f"{number / GB:.2f}G"
    if number >= 1024 ** 2:
        return f"{number / (1024 ** 2):.0f}M"
    return str(number)


def progress_bar(percent: int, width: int = 10) -> str:
    percent = max(0, min(100, int(percent)))
    filled = int(round((percent / 100) * width))
    return "█" * filled + "░" * (width - filled)


def short_digest(value: str) -> str:
    if not value:
        return ""
    value = value.strip()
    if value.startswith("sha256:"):
        return value[7:19]
    if value.startswith("sha256"):
        return value[6:18]
    return value[:12]


def image_tag(image: str) -> str:
    last = image.rsplit("/", 1)[-1]
    return last.rsplit(":", 1)[-1] if ":" in last else "latest"


def is_fixed_tag(image: str) -> bool:
    tag = image_tag(image)
    return tag not in FLOATING_TAGS and bool(FIXED_TAG_RE.match(tag))


def is_data_image(image: str, labels: Dict[str, str]) -> bool:
    lower = image.lower()
    service = labels.get("com.docker.compose.service", "").lower()
    return any(x in lower for x in ("postgres", "redis", "mysql", "mariadb", "mongo")) or service in {"postgres", "redis", "db", "database"}


def skip_remote_digest(image: str, labels: Dict[str, str]) -> bool:
    return image.startswith("sha256:") or is_fixed_tag(image) or is_data_image(image, labels)


def docker_ps_names() -> List[str]:
    rc, out, _ = run(["docker", "ps", "--format", "{{.Names}}"])
    if rc != 0:
        return []
    return [line.strip() for line in out.splitlines() if line.strip()]


def docker_inspect(name: str) -> Optional[Dict[str, Any]]:
    rc, out, _ = run(["docker", "inspect", name])
    if rc != 0 or not out:
        return None
    try:
        data = json.loads(out)
        return data[0] if data else None
    except Exception:
        return None


def image_inspect(image: str) -> Optional[Dict[str, Any]]:
    rc, out, _ = run(["docker", "image", "inspect", image])
    if rc != 0 or not out:
        return None
    try:
        data = json.loads(out)
        return data[0] if data else None
    except Exception:
        return None


def local_digest(info: Dict[str, Any]) -> str:
    for item in info.get("RepoDigests") or []:
        if "@sha256:" in item:
            return item.rsplit("@", 1)[-1]
    return info.get("Id", "")


def remote_digest(image: str) -> Tuple[Optional[str], str]:
    rc, out, err = run(["docker", "buildx", "imagetools", "inspect", image, "--format", "{{json .Manifest}}"], timeout=35)
    if rc != 0 or not out:
        return None, safe(err or out)
    try:
        data = json.loads(out)
        digest = data.get("digest") or data.get("Digest")
        return (digest, "") if digest else (None, "no digest in manifest")
    except Exception as exc:
        return None, f"json parse failed: {safe(exc)}"


def docker_results() -> List[CheckResult]:
    adapters = load_adapter_states()
    results: List[CheckResult] = []
    for name in docker_ps_names():
        info = docker_inspect(name)
        if not info:
            results.append(CheckResult(name, "docker", "check-failed", note="inspect failed"))
            continue
        config = info.get("Config") or {}
        labels = config.get("Labels") or {}
        image = config.get("Image") or ""
        if labels.get("autoupgrade.enable") == "true":
            category = "auto-upgrade"
        elif labels.get("diun.enable") == "true":
            category = "notify-only"
        else:
            category = "manual-only"
        state = adapters.get(name) if category == "auto-upgrade" else {}
        state_status = str(state.get("status") or "")
        state_note = str(state.get("note") or "")
        image_info = image_inspect(image)
        if not image_info:
            results.append(CheckResult(name, "docker", "check-failed", category=category, note="image inspect failed"))
            continue
        current = short_digest(local_digest(image_info))
        if state_status in {"adapter-blocked", "failed", "rolled-back"}:
            results.append(CheckResult(name, "docker", state_status, current=current, category=category, note=state_note or image))
            continue
        if skip_remote_digest(image, labels):
            status = state_status if state_status in {"current", "upgraded"} else "checked"
            results.append(CheckResult(name, "docker", status, current=current, category=category, note=state_note or image))
            continue
        latest, err = remote_digest(image)
        if not latest:
            if state_status in {"current", "upgraded"}:
                note = state_note or f"registry digest unavailable after adapter state={state_status}: {err}"
                results.append(CheckResult(name, "docker", state_status, current=current, category=category, note=note))
                continue
            results.append(CheckResult(name, "docker", "check-failed", current=current, category=category, note=err))
            continue
        latest_short = short_digest(latest)
        status = "update-available" if current and latest_short and current != latest_short else "current"
        results.append(CheckResult(name, "docker", status, current=current, latest=latest_short, category=category, note=image))
    return results


def first_version(text: str) -> str:
    match = re.search(r"v?\d+(?:\.\d+){1,4}(?:[-_A-Za-z0-9.]+)?", text)
    return match.group(0) if match else ""


def version_tuple(value: str) -> Optional[Tuple[int, ...]]:
    match = re.search(r"(\d+(?:\.\d+){1,4})", value or "")
    if not match:
        return None
    return tuple(int(part) for part in match.group(1).split("."))


def version_status(current: str, latest: str) -> str:
    cur = version_tuple(current)
    lat = version_tuple(latest)
    if cur and lat:
        return "update-available" if cur < lat else "current"
    if current and latest:
        return "current" if current == latest else "update-available"
    return "checked"


def github_latest(repo: str) -> Tuple[str, str]:
    req = urllib.request.Request(f"https://api.github.com/repos/{repo}/releases/latest", headers={"User-Agent": "daily-update-report/1.0"})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        return str(data.get("tag_name") or ""), str(data.get("html_url") or "")
    except Exception as exc:
        return "", safe(exc)


def onepanel_latest() -> str:
    try:
        with urllib.request.urlopen("https://resource.1panel.pro/v2/stable/latest", timeout=TIMEOUT) as resp:
            return resp.read().decode("utf-8").strip()
    except Exception:
        latest, _ = github_latest("1Panel-dev/1Panel")
        return latest


def nondocker_results(host_label: str) -> List[CheckResult]:
    adapters = load_adapter_states()
    results: List[CheckResult] = []

    def add_adapter(name: str, current: str, latest: str, fallback_note: str = "") -> None:
        state = adapters.get(name) or {}
        status = version_status(current, latest)
        note = fallback_note
        if state.get("status") in {"current", "upgraded", "rolled-back", "adapter-blocked", "failed"}:
            status = str(state.get("status"))
            note = str(state.get("note") or note)
            current = str(state.get("old") or current)
            latest = str(state.get("new") or latest)
        elif status == "update-available":
            status = "adapter-blocked"
            note = "adapter has not completed yet"
        results.append(CheckResult(name, "non-docker", status, current=safe(current, 80), latest=safe(latest, 80), category="host-adapter", note=safe(note, 120)))

    rc, out, _ = run(["docker", "version", "--format", "{{.Server.Version}}"])
    docker_current = out if rc == 0 else ""
    docker_latest, docker_note = github_latest("moby/moby")
    add_adapter("docker-engine", docker_current, docker_latest, docker_note)

    rc, out, _ = run(["docker", "compose", "version", "--short"])
    compose_current = out if rc == 0 else ""
    compose_latest, compose_note = github_latest("docker/compose")
    add_adapter("docker-compose", compose_current, compose_latest, compose_note)

    if host_label.upper() == "LAX":
        rc, out, _ = run(["1pctl", "version"])
        current = ""
        if rc == 0:
            match = re.search(r"version:\s*(v?\S+)", out)
            current = match.group(1) if match else first_version(out)
        add_adapter("1panel", current, onepanel_latest())
    return results


def resolve_capacity_profile(host_label: str, explicit: str = "") -> str:
    if explicit:
        return explicit
    candidate = Path(f"/etc/vps-capacity-guard/{host_label.lower()}.json")
    return str(candidate) if candidate.exists() else ""


def capacity_snapshot(host_label: str, profile_path: str) -> Optional[Dict[str, Any]]:
    guard = "/usr/local/bin/vps-capacity-guard.py"
    if not profile_path or not Path(guard).exists():
        return None
    rc, out, err = run([guard, "--profile", profile_path, "--snapshot-json", "--no-telegram"], timeout=90)
    if rc != 0 or not out:
        return {"state": "unknown", "error": safe(err or out or f"rc={rc}"), "host_label": host_label.upper()}
    try:
        return json.loads(out)
    except Exception as exc:
        return {"state": "unknown", "error": f"json parse failed: {safe(exc)}", "host_label": host_label.upper()}


def capacity_icon(state: str) -> str:
    return {"ok": "🟢", "watch": "🟡", "warn": "🟠", "clean": "🧹", "critical": "🔴"}.get(state, "⚪")


def capacity_lines(capacity: Optional[Dict[str, Any]]) -> List[str]:
    if not capacity:
        return ["💾 空间自检", "• ⚪ 未接入容量守护快照"]
    if capacity.get("state") == "unknown":
        return ["💾 空间自检", f"• ⚪ 快照读取失败：{safe(capacity.get('error'), 80)}", "• 🛡️ 日报只提醒，不会清理。"]
    state = str(capacity.get("state", "unknown"))
    used_pct = int(capacity.get("used_pct", 0))
    health_bad = [
        value.get("label", name)
        for name, value in (capacity.get("health") or {}).items()
        if isinstance(value, dict) and value.get("status") != "ok"
    ]
    dirs = [item for item in capacity.get("dirs", []) if isinstance(item, dict) and item.get("bytes") is not None]
    dirs.sort(key=lambda item: int(item.get("bytes", 0)), reverse=True)
    top = "、".join(f"{safe(item.get('label'), 24)} {human_bytes(item.get('bytes'))}" for item in dirs[:3]) or "暂无"
    health_text = "全部正常" if not health_bad else "异常：" + "、".join(safe(x, 24) for x in health_bad[:5])
    return [
        "💾 空间自检",
        f"• {capacity_icon(state)} {state} ｜ {progress_bar(used_pct)} {used_pct}% ｜ 可用 {human_bytes(capacity.get('free'))}",
        f"• 🩺 关键服务：{health_text}",
        f"• 🗂️ 空间大头：{top}",
        "• 🛡️ 日报只读；需要清理时由容量守护或人工确认处理。",
    ]


def names_text(results: List[CheckResult], limit: int = 8) -> str:
    names = [safe(r.name, 48) for r in results if r.name]
    if not names:
        return "暂无"
    suffix = "" if len(names) <= limit else f" 等 {len(names)} 个"
    return ", ".join(names[:limit]) + suffix


def summarize(host_label: str, docker: List[CheckResult], nondocker: List[CheckResult], capacity: Optional[Dict[str, Any]]) -> str:
    docker_updates = [r for r in docker if r.status == "update-available"]
    failed = [r for r in docker + nondocker if r.status == "check-failed" or r.status == "failed"]
    adapter_items = [r for r in nondocker if r.category == "host-adapter"]
    adapter_attention = [r for r in adapter_items if r.status in {"update-available", "adapter-blocked", "rolled-back", "failed"}]
    docker_adapter_attention = [r for r in docker if r.status in {"adapter-blocked", "rolled-back"}]
    auto_updates = [r for r in docker_updates if r.category == "auto-upgrade"]
    notify_updates = [r for r in docker_updates if r.category == "notify-only"]
    manual_updates = [r for r in docker_updates if r.category not in {"auto-upgrade", "notify-only"}]
    by_cat: Dict[str, int] = {}
    for r in docker:
        by_cat[r.category or "unknown"] = by_cat.get(r.category or "unknown", 0) + 1
    capacity_state = capacity.get("state") if capacity else ""
    capacity_attention = capacity_state in {"warn", "clean", "critical", "unknown"}
    action_count = len(auto_updates) + len(notify_updates) + len(manual_updates) + len(adapter_attention) + len(docker_adapter_attention) + len(failed)
    conclusion_icon = "🟢" if not action_count and not capacity_attention else "🟡"
    if failed or capacity_state == "critical":
        conclusion_icon = "🔴"
    conclusion = "目前不用手动处理" if conclusion_icon == "🟢" else f"有 {action_count} 项更新/检查需要看，空间状态 {capacity_state or '未接入'}"
    class_parts = [f"{CATEGORY_LABELS.get(k, k)} {by_cat[k]} 个" for k in ("auto-upgrade", "notify-only", "manual-only", "unknown") if by_cat.get(k)]
    lines = [
        f"📋【{host_label.upper()} 每日更新检查】",
        f"🕒 {dt.datetime.now().strftime('%Y-%m-%d %H:%M')}",
        "━━━━━━━━━━━━",
        f"{conclusion_icon} 结论：{conclusion}",
        "",
        "📌 今日重点",
        f"• 👀 需要处理：{action_count} 项",
        f"• 🆕 发现新版本：{len(docker_updates)} 个",
        f"• ❌ 检查失败：{len(failed)} 个",
        "",
        "━━━━━━━━━━━━",
        *capacity_lines(capacity),
        "",
        "━━━━━━━━━━━━",
        "📦 Docker 服务",
        f"• ✅ 已检查 {len(docker)} 个 ｜ 🆕 新版本 {len(docker_updates)} 个",
        f"• 🏷️ {' ｜ '.join(class_parts) if class_parts else '暂无 Docker 容器'}",
        "",
        "🧩 系统/面板组件",
        f"• ✅ 已检查 {len(nondocker)} 个 ｜ ❌ 失败 {len([r for r in nondocker if r.status in {'check-failed', 'failed'}])} 个",
        "• 🛠️ 自动适配：" + (", ".join(f"{safe(r.name, 48)}={safe(r.status, 32)}" for r in adapter_items) if adapter_items else "暂无"),
        "",
        "━━━━━━━━━━━━",
        "👀 需要你看",
    ]
    if auto_updates:
        lines.append("• 🚀 自动升级服务发现新版本：" + names_text(auto_updates))
    if notify_updates:
        lines.append("• 🔔 只提醒不自动升：" + names_text(notify_updates))
    if manual_updates:
        lines.append("• 🛠️ 需要手动确认：" + names_text(manual_updates))
    if docker_adapter_attention:
        lines.append("• 🚧 自动升级服务适配需要看：" + names_text(docker_adapter_attention))
    if adapter_attention:
        lines.append("• 🛠️ 自动适配需要看：" + names_text(adapter_attention))
    if failed:
        lines.append("• ❌ 检查失败：" + names_text(failed))
    if not action_count:
        lines.append("• ✅ 暂无")
    lines.extend([
        "",
        "🛡️ 安全边界",
        "这条日报只负责检查和提醒；系统/面板组件只由具备 rollback/health gate 的 adapter 执行，日报本身不会执行清理或升级。",
    ])
    return "\n".join(lines)


def detail_lines(host_label: str, docker: List[CheckResult], nondocker: List[CheckResult], capacity: Optional[Dict[str, Any]]) -> List[str]:
    ts = dt.datetime.now().isoformat(timespec="seconds")
    lines = [f"{ts} host={host_label.upper()} report-start"]
    if capacity:
        lines.append(f"{ts} host={host_label.upper()} capacity state={safe(capacity.get('state'))} free={safe(capacity.get('free'), 32)} used_pct={safe(capacity.get('used_pct'), 32)}")
    for r in docker + nondocker:
        lines.append(f"{ts} host={host_label.upper()} kind={r.kind} name={safe(r.name)} category={safe(r.category)} status={safe(r.status)} current={safe(r.current, 32)} latest={safe(r.latest, 32)} note={safe(r.note)}")
    lines.append(f"{ts} host={host_label.upper()} report-end")
    return lines


def load_env_file(path: str) -> Dict[str, str]:
    values: Dict[str, str] = {}
    p = Path(path)
    if not p.exists():
        return values
    for line in p.read_text(errors="ignore").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip('"').strip("'")
    return values


def load_env_from_container(name: str) -> Dict[str, str]:
    rc, out, _ = run(["docker", "inspect", name, "--format", "{{json .Config.Env}}"])
    if rc != 0 or not out:
        return {}
    try:
        env_list = json.loads(out)
    except Exception:
        return {}
    values: Dict[str, str] = {}
    for item in env_list or []:
        if "=" in item:
            key, value = item.split("=", 1)
            values[key] = value
    return values


def telegram_env() -> Tuple[str, str]:
    candidates = ["/opt/frontier/apps/diun/.env", "/opt/shared-hy2/.env", "/opt/shared-hy2/env", "/opt/shared-hy2/.secrets.env"]
    merged: Dict[str, str] = {}
    for path in candidates:
        merged.update(load_env_file(path))
    for container in ("diun", "shared-hy2-diun"):
        merged.update(load_env_from_container(container))
    token = os.environ.get("TG_TOKEN") or os.environ.get("DIUN_NOTIF_TELEGRAM_TOKEN") or merged.get("TG_TOKEN") or merged.get("DIUN_NOTIF_TELEGRAM_TOKEN") or ""
    chat = os.environ.get("TG_CHATID") or os.environ.get("TG_CHAT_ID") or os.environ.get("DIUN_NOTIF_TELEGRAM_CHATIDS") or merged.get("TG_CHATID") or merged.get("TG_CHAT_ID") or merged.get("DIUN_NOTIF_TELEGRAM_CHATIDS") or ""
    return token, chat


def send_telegram(text: str) -> bool:
    token, chat = telegram_env()
    if not token or not chat:
        return False
    url = f"https://api.telegram.org/bot{token}/sendMessage"
    payload = urllib.parse.urlencode({"chat_id": chat, "parse_mode": "HTML", "text": html.escape(text), "disable_web_page_preview": "true"}).encode("utf-8")
    try:
        req = urllib.request.Request(url, data=payload, method="POST")
        with urllib.request.urlopen(req, timeout=TIMEOUT) as resp:
            return 200 <= resp.status < 300
    except Exception:
        return False


def append_log(path: str, lines: Iterable[str]) -> None:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    with p.open("a", encoding="utf-8") as fh:
        for line in lines:
            fh.write(line + "\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host-label", default=os.environ.get("HOST_LABEL", "HOST"))
    parser.add_argument("--no-telegram", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--send-test", action="store_true")
    parser.add_argument("--log-file", default=LOG_DEFAULT)
    parser.add_argument("--capacity-profile", default="")
    args = parser.parse_args()

    if args.send_test:
        text = f"🧪【{args.host_label.upper()} 每日更新检查】测试消息\n✅ Telegram 通知通道正常"
        ok = False if args.no_telegram else send_telegram(text)
        print("telegram_test=" + ("ok" if ok else "failed_or_disabled"))
        return 0 if ok or args.no_telegram else 1

    docker = docker_results()
    nondocker = nondocker_results(args.host_label)
    capacity = capacity_snapshot(args.host_label, resolve_capacity_profile(args.host_label, args.capacity_profile))
    summary = summarize(args.host_label, docker, nondocker, capacity)
    append_log(args.log_file, detail_lines(args.host_label, docker, nondocker, capacity))
    if args.dry_run or args.no_telegram:
        print(summary)
    if not args.no_telegram:
        ok = send_telegram(summary)
        append_log(args.log_file, [f"{dt.datetime.now().isoformat(timespec='seconds')} host={args.host_label.upper()} telegram ok={ok}"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
