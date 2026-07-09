import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from vibe_flow.fixtures import create_fixture, write_codestable_base, write_feature, write_trellis
from vibe_flow.paths import strip_date_prefix
from vibe_flow.trellis_bridge import BRIDGED_CODEX_MODE_RULE, BRIDGED_WORKFLOW_RULE, LEGACY_CODEX_MODE_RULE, LEGACY_WORKFLOW_RULE


class VibeFlowCliTests(unittest.TestCase):
    def test_cli_version(self):
        proc = subprocess.run(
            [sys.executable, "-m", "vibe_flow", "--version"],
            text=True,
            encoding="utf-8",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("vibe-flow 0.1.6", proc.stdout)

    def test_strip_date_prefix_keeps_full_slug(self):
        self.assertEqual(strip_date_prefix("07-08-export-csv"), "export-csv")
        self.assertEqual(strip_date_prefix("2026-07-08-vibe-flow-bridge-cli"), "vibe-flow-bridge-cli")
        self.assertEqual(strip_date_prefix("plain-slug"), "plain-slug")

    def test_doctor_reports_missing_surfaces(self):
        with tempfile.TemporaryDirectory() as tmp:
            payload = run_json(tmp, "doctor", "--json")
        self.assertEqual(payload["schema_version"], 1)
        codes = {item["code"] for item in payload["diagnostics"]}
        self.assertIn("trellis_missing", codes)
        self.assertIn("codestable_missing", codes)

    def test_explicit_codestable_intent_is_passthrough(self):
        with tempfile.TemporaryDirectory() as tmp:
            create_fixture(Path(tmp), "codestable_only_design")
            payload = run_json(tmp, "next", "--intent", "cs-feat-design", "--json")
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["label"], "cs-feat-design")
        self.assertEqual(payload["next_action"]["commands"], [])

    def test_generic_cs_plugin_mention_is_passthrough(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            payload = run_json(
                tmp,
                "next",
                "--intent",
                "[$codestable:cs](C:\\skills\\cs\\SKILL.md) 允许进入规划阶段",
                "--json",
            )
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["label"], "cs")

    def test_explicit_cs_skill_wins_over_continue_keyword(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)
            payload = run_json(
                tmp,
                "next",
                "--intent",
                "[$codestable:cs-roadmap](C:\\skills\\cs-roadmap\\SKILL.md) 继续规划",
                "--json",
            )
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["label"], "cs-roadmap")

    def test_csharp_file_extension_is_not_generic_cs_intent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)
            payload = run_json(tmp, "next", "--intent", "修改 Foo.cs 后继续", "--json")
        self.assertEqual(payload["mode"], "bridge_autopilot")
        self.assertEqual(payload["next_action"]["label"], "cs-feat-design-review")

    def test_confirmed_commit_archive_is_a_direct_owner_action(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_trellis(root, status="in_progress", task="07-08-vibe-flow-bridge-cli")
            write_codestable_base(root)
            payload = run_json(
                tmp,
                "next",
                "--intent",
                "确认只提交并归档 vibe-flow 任务",
                "--json",
            )
        self.assertEqual(payload["next_action"]["kind"], "stop")
        self.assertEqual(payload["next_action"]["label"], "direct-owner-action")
        self.assertEqual(payload["next_action"]["requested_action"], "commit_and_archive")
        self.assertFalse(payload["next_action"]["requires_owner"])

    def test_unconfirmed_submit_word_is_not_a_direct_owner_action(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            payload = run_json(tmp, "next", "--intent", "提交一个导出功能", "--json")
        self.assertEqual(payload["next_action"]["label"], "cs-feat")

    def test_design_draft_routes_to_design_review(self):
        with tempfile.TemporaryDirectory() as tmp:
            create_fixture(Path(tmp), "codestable_only_design")
            payload = run_json(tmp, "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["mode"], "bridge_autopilot")
        self.assertEqual(payload["codestable"]["stage"], "design_draft")
        self.assertEqual(payload["next_action"]["label"], "cs-feat-design-review")

    def test_design_review_without_approval_stops_for_owner(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True, design_review=True)
            payload = run_json(tmp, "next", "--intent", "继续", "--json")
        self.assertEqual(payload["codestable"]["stage"], "design_review_passed")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertTrue(payload["next_action"]["requires_owner"])

    def test_approved_design_with_trellis_planning_proposes_start(self):
        with tempfile.TemporaryDirectory() as tmp:
            create_fixture(Path(tmp), "both_approved_planning")
            payload = run_json(tmp, "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "approved")
        self.assertEqual(payload["next_action"]["kind"], "run_command")
        command = payload["next_action"]["commands"][0]
        self.assertTrue(command["safe"])
        self.assertIn(".trellis/scripts/task.py", " ".join(command["argv"]))

    def test_goal_wrapper_uses_child_feature_stage(self):
        with tempfile.TemporaryDirectory() as tmp:
            create_fixture(Path(tmp), "goal_wraps_feature")
            payload = run_json(tmp, "next", "--intent", "用 cs-goal 接管这个 feature", "--json")
        self.assertEqual(payload["mode"], "bridge_autopilot")
        self.assertEqual(payload["codestable"]["unit_type"], "feature")
        self.assertEqual(payload["codestable"]["stage"], "implementation_done")
        self.assertEqual(payload["next_action"]["label"], "cs-code-review")

    def test_review_and_qa_stage_map(self):
        cases = [
            ("failed", None, "review_failed", "cs-feat-impl", "review_fix"),
            ("passed", None, "review_passed", "cs-feat-qa", None),
            ("passed", "failed", "qa_failed", "cs-feat-impl", "qa_fix"),
            ("passed", "passed", "qa_passed", "cs-feat-accept", None),
        ]
        for review, qa, stage, label, mode in cases:
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                write_codestable_base(root)
                write_feature(root, "2026-07-08-export-csv", design=True, approved=True, implementation_done=True, review=review, qa=qa)
                payload = run_json(tmp, "next", "--intent", "继续", "--json")
                self.assertEqual(payload["codestable"]["stage"], stage)
                self.assertEqual(payload["next_action"]["label"], label)
                if mode:
                    self.assertEqual(payload["next_action"]["mode"], mode)

    def test_self_review_blocks_acceptance_after_qa(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                reviewer="self",
                qa="passed",
            )
            payload = run_json(tmp, "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "qa_passed")
        self.assertEqual(payload["codestable"]["gates"]["implementation_review"]["status"], "requires_task_agent")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["label"], "implementation-review-needs-task-agent")

    def test_explicit_accept_does_not_bypass_self_review_blocker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                reviewer="self",
                qa="passed",
            )
            payload = run_json(tmp, "next", "--intent", "cs-feat-accept", "--json")
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["requested_label"], "cs-feat-accept")

    def test_explicit_code_review_can_resolve_review_blocker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                reviewer="self",
                qa="passed",
            )
            payload = run_json(tmp, "next", "--intent", "cs-code-review", "--json")
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["kind"], "invoke_skill")
        self.assertEqual(payload["next_action"]["label"], "cs-code-review")

    def test_blocked_goal_wrapper_blocks_child_acceptance(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
            )
            goal = root / ".codestable" / "goals" / "2026-07-08-export-csv-implementation"
            goal.mkdir(parents=True, exist_ok=True)
            (goal / "state.yaml").write_text(
                "schema_version: 1\n"
                "goal: export-csv-implementation\n"
                "status: blocked\n"
                "child_unit: .codestable/features/2026-07-08-export-csv\n"
                "blocker_signature: implementation_review_requires_task_agent\n",
                encoding="utf-8",
            )
            payload = run_json(tmp, "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "qa_passed")
        self.assertTrue(payload["codestable"]["wrapper_unit"]["blocked"])
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["label"], "implementation-review-needs-task-agent")

    def test_accepted_feature_blocks_trellis_closeout_when_external_worktree_is_not_git(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "vault"
            external = Path(tmp) / "tool"
            external.mkdir(parents=True)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
                acceptance="passed",
            )
            write_external_worktree_override(root, "2026-07-08-export-csv", external)
            payload = run_json(str(root), "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "accepted")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["label"], "external-worktree-git-required")

    def test_accepted_feature_blocks_trellis_closeout_when_codestable_evidence_is_dirty(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "vault"
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
            )
            init_clean_git_repo(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
                acceptance="passed",
            )
            payload = run_json(str(root), "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "accepted")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["label"], "codestable-evidence-commit-needed")

    def test_accepted_feature_blocks_trellis_closeout_when_external_worktree_is_dirty(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "vault"
            external = Path(tmp) / "tool"
            init_clean_git_repo(external)
            (external / "dirty.txt").write_text("dirty\n", encoding="utf-8")
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
                acceptance="passed",
            )
            write_external_worktree_override(root, "2026-07-08-export-csv", external)
            payload = run_json(str(root), "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertEqual(payload["next_action"]["label"], "external-worktree-commit-needed")
        self.assertFalse(payload["codestable"]["external_worktrees"][0]["is_clean"])

    def test_accepted_feature_allows_trellis_closeout_when_external_worktree_is_clean(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "vault"
            external = Path(tmp) / "tool"
            init_clean_git_repo(external)
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
                acceptance="passed",
            )
            write_external_worktree_override(root, "2026-07-08-export-csv", external)
            payload = run_json(str(root), "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "accepted")
        self.assertEqual(payload["next_action"]["label"], "trellis-check")
        self.assertTrue(payload["codestable"]["external_worktrees"][0]["is_git_repo"])
        self.assertTrue(payload["codestable"]["external_worktrees"][0]["is_clean"])

    def test_accepted_feature_allows_trellis_closeout_when_codestable_evidence_is_clean(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "vault"
            write_codestable_base(root)
            write_feature(
                root,
                "2026-07-08-export-csv",
                design=True,
                approved=True,
                implementation_done=True,
                review="passed",
                qa="passed",
                acceptance="passed",
            )
            init_clean_git_repo(root)
            payload = run_json(str(root), "next", "--intent", "继续完成剩下的", "--json")
        self.assertEqual(payload["codestable"]["stage"], "accepted")
        self.assertEqual(payload["next_action"]["label"], "trellis-check")

    def test_multiple_active_units_are_ambiguous(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)
            write_feature(root, "2026-07-08-import-csv", design=True)
            payload = run_json(tmp, "next", "--intent", "继续", "--json")
        self.assertTrue(payload["codestable"]["ambiguous"])
        self.assertEqual(payload["next_action"]["kind"], "ask_owner")
        self.assertGreaterEqual(len(payload["next_action"]["candidates"]), 2)

    def test_unmatched_current_trellis_task_does_not_fall_back_to_old_units(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_trellis(root, status="planning", task="07-10-sjc-lax-remote-manual-upgrade")
            write_codestable_base(root)
            write_feature(root, "2026-07-08-windows-cleanup", acceptance="passed")

            goal = root / ".codestable" / "goals" / "2026-07-08-windows-cleanup-goal"
            goal.mkdir(parents=True, exist_ok=True)
            (goal / "state.yaml").write_text(
                "status: complete\n"
                "child_unit: .codestable/features/2026-07-08-windows-cleanup\n",
                encoding="utf-8",
            )

            issue = root / ".codestable" / "issues" / "2026-07-07-old-regression"
            issue.mkdir(parents=True, exist_ok=True)
            (issue / "state.yaml").write_text("status: active\n", encoding="utf-8")

            payload = run_json(tmp, "next", "--intent", "规划当前任务", "--json")

        self.assertIsNone(payload["codestable"]["primary_unit"])
        self.assertEqual(
            payload["codestable"]["selection"]["status"],
            "trellis_current_task_unmatched",
        )
        self.assertEqual(payload["next_action"]["label"], "cs-roadmap")
        self.assertNotEqual(payload["next_action"]["label"], "trellis-check")
        self.assertNotEqual(payload["next_action"]["label"], "cs-issue")

    def test_active_goal_wrapper_does_not_hide_unrelated_active_unit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)

            goal = root / ".codestable" / "goals" / "2026-07-08-export-csv-goal"
            goal.mkdir(parents=True, exist_ok=True)
            (goal / "state.yaml").write_text(
                "status: active\n"
                "child_unit: .codestable/features/2026-07-08-export-csv\n",
                encoding="utf-8",
            )

            issue = root / ".codestable" / "issues" / "2026-07-08-unrelated-issue"
            issue.mkdir(parents=True, exist_ok=True)
            (issue / "state.yaml").write_text("status: active\n", encoding="utf-8")

            payload = run_json(tmp, "next", "--intent", "继续", "--json")

        self.assertTrue(payload["codestable"]["ambiguous"])
        self.assertIsNone(payload["codestable"]["primary_unit"])
        self.assertEqual(payload["next_action"]["label"], "choose-codestable-unit")

    def test_completed_issue_reports_do_not_hide_active_roadmap(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)

            issue = root / ".codestable" / "issues" / "2026-07-07-old-regression"
            issue.mkdir(parents=True, exist_ok=True)
            (issue / "old-regression-fix-note.md").write_text(
                "---\ndoc_type: issue-fix\n---\n# Fix\n",
                encoding="utf-8",
            )
            (issue / "old-regression-review.md").write_text(
                "---\nstatus: passed\nreviewer: subagent\n---\n# Review\n",
                encoding="utf-8",
            )

            roadmap = root / ".codestable" / "roadmap" / "new-upgrade-plan"
            roadmap.mkdir(parents=True, exist_ok=True)
            (roadmap / "new-upgrade-plan-roadmap.md").write_text(
                "---\ndoc_type: roadmap\nstatus: active\n---\n# Roadmap\n",
                encoding="utf-8",
            )

            payload = run_json(tmp, "next", "--intent", "继续", "--json")

        issue_candidate = next(
            item for item in payload["codestable"]["candidates"]
            if item["unit_type"] == "issue"
        )
        self.assertEqual(issue_candidate["stage"], "completed")
        self.assertEqual(payload["codestable"]["unit_type"], "roadmap")
        self.assertEqual(payload["codestable"]["stage"], "active")
        self.assertEqual(payload["next_action"]["label"], "cs-roadmap")

    def test_self_reviewed_issue_is_not_marked_completed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            issue = root / ".codestable" / "issues" / "2026-07-08-review-needed"
            issue.mkdir(parents=True, exist_ok=True)
            (issue / "review-needed-fix-note.md").write_text(
                "---\ndoc_type: issue-fix\n---\n# Fix\n",
                encoding="utf-8",
            )
            (issue / "review-needed-review.md").write_text(
                "---\nstatus: passed\nreviewer: self\n---\n# Review\n",
                encoding="utf-8",
            )
            payload = run_json(tmp, "next", "--intent", "继续", "--json")

        self.assertEqual(payload["codestable"]["stage"], "review_requires_task_agent")
        self.assertEqual(payload["next_action"]["label"], "cs-issue")

    def test_trellis_current_task_disambiguates_codestable_unit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_trellis(root, status="planning", task="07-08-import-csv")
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)
            write_feature(root, "2026-07-08-import-csv", design=True, design_review=True)
            payload = run_json(tmp, "next", "--intent", "继续", "--json")
        self.assertFalse(payload["codestable"]["ambiguous"])
        self.assertEqual(payload["codestable"]["slug"], "import-csv")
        self.assertEqual(payload["next_action"]["label"], "approve-design")

    def test_resolved_current_task_is_not_session_ambiguous(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_trellis(root, status="planning", task="07-08-export-csv")
            sessions = root / ".trellis" / ".runtime" / "sessions"
            (sessions / "codex-old.json").write_text("{}\n", encoding="utf-8")
            (sessions / "codex-current.json").write_text("{}\n", encoding="utf-8")
            payload = run_json(tmp, "status", "--json")

        self.assertEqual(payload["trellis"]["session_pointer_count"], 2)
        self.assertEqual(payload["trellis"]["current_task"], "07-08-export-csv")
        self.assertFalse(payload["trellis"]["session_ambiguous"])

    def test_explicit_codestable_intent_bypasses_unit_ambiguity(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True)
            write_feature(root, "2026-07-08-import-csv", design=True)
            payload = run_json(tmp, "next", "--intent", "cs-feat-design", "--json")
        self.assertEqual(payload["mode"], "codestable_passthrough")
        self.assertEqual(payload["next_action"]["label"], "cs-feat-design")
        self.assertEqual(payload["next_action"]["commands"], [])

    def test_init_creates_config_and_managed_agents_block(self):
        with tempfile.TemporaryDirectory() as tmp:
            payload = run_json(tmp, "init", "--json")
            root = Path(tmp)
            self.assertTrue((root / ".vibe-flow" / "config.yaml").exists())
            agents = (root / "AGENTS.md").read_text(encoding="utf-8")
        self.assertTrue(payload["ok"])
        self.assertIn("VIBE-FLOW:START", agents)
        self.assertIn("vibe status --json", agents)

    def test_init_patches_trellis_inline_bridge_text(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_legacy_trellis_bridge_files(root)
            payload = run_json(tmp, "init", "--json")
            workflow = (root / ".trellis" / "workflow.md").read_text(encoding="utf-8")
            hook = (root / ".codex" / "hooks" / "inject-workflow-state.py").read_text(encoding="utf-8")
        self.assertEqual(payload["init"]["trellis_bridge"]["workflow"], "patched")
        self.assertEqual(payload["init"]["trellis_bridge"]["codex_hook"], "patched")
        self.assertIn(BRIDGED_WORKFLOW_RULE, workflow)
        self.assertIn(BRIDGED_CODEX_MODE_RULE, hook)
        self.assertNotIn(LEGACY_WORKFLOW_RULE, workflow)
        self.assertNotIn(LEGACY_CODEX_MODE_RULE, hook)

    def test_init_bridge_patch_is_idempotent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_legacy_trellis_bridge_files(root)
            first = run_json(tmp, "init", "--json")
            second = run_json(tmp, "init", "--json")
            workflow = (root / ".trellis" / "workflow.md").read_text(encoding="utf-8")
            hook = (root / ".codex" / "hooks" / "inject-workflow-state.py").read_text(encoding="utf-8")
        self.assertEqual(first["init"]["trellis_bridge"]["workflow"], "patched")
        self.assertEqual(first["init"]["trellis_bridge"]["codex_hook"], "patched")
        self.assertEqual(second["init"]["trellis_bridge"]["workflow"], "already_patched")
        self.assertEqual(second["init"]["trellis_bridge"]["codex_hook"], "already_patched")
        self.assertEqual(workflow.count(BRIDGED_WORKFLOW_RULE), 1)
        self.assertEqual(hook.count(BRIDGED_CODEX_MODE_RULE), 1)

    def test_doctor_warns_when_trellis_bridge_patch_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_legacy_trellis_bridge_files(root)
            payload = run_json(tmp, "doctor", "--json")
        codes = {item["code"] for item in payload["diagnostics"]}
        self.assertIn("trellis_workflow_bridge_missing", codes)
        self.assertIn("codex_hook_bridge_missing", codes)

    def test_doctor_clean_after_bridge_patch(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_legacy_trellis_bridge_files(root)
            run_json(tmp, "init", "--json")
            payload = run_json(tmp, "doctor", "--json")
        codes = {item["code"] for item in payload["diagnostics"]}
        self.assertNotIn("trellis_workflow_bridge_missing", codes)
        self.assertNotIn("codex_hook_bridge_missing", codes)

    def test_run_next_refuses_owner_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            write_codestable_base(root)
            write_feature(root, "2026-07-08-export-csv", design=True, design_review=True)
            payload = run_json(tmp, "run", "next", "--intent", "继续", "--json")
        self.assertFalse(payload["run"]["executed"])
        self.assertIn("owner", payload["run"]["reason"])

    def test_run_next_executes_only_safe_allowlisted_command(self):
        with tempfile.TemporaryDirectory() as tmp:
            create_fixture(Path(tmp), "both_approved_planning")
            payload = run_json(tmp, "run", "next", "--intent", "继续", "--json")
        self.assertTrue(payload["run"]["executed"])
        self.assertEqual(payload["run"]["results"][0]["returncode"], 0)

    def test_trellis_only_routes_to_onboard(self):
        with tempfile.TemporaryDirectory() as tmp:
            write_trellis(Path(tmp), status="planning", task="07-08-export-csv")
            payload = run_json(tmp, "next", "--intent", "加 CSV 导出", "--json")
        self.assertEqual(payload["mode"], "trellis_first")
        self.assertEqual(payload["next_action"]["label"], "cs-onboard")


def run_json(root: str, *args: str) -> dict:
    proc = subprocess.run(
        [sys.executable, "-m", "vibe_flow", "--root", root, *args],
        text=True,
        encoding="utf-8",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise AssertionError(f"command failed: {proc.stderr}\n{proc.stdout}")
    return json.loads(proc.stdout)


def write_legacy_trellis_bridge_files(root: Path) -> None:
    workflow = root / ".trellis" / "workflow.md"
    workflow.parent.mkdir(parents=True, exist_ok=True)
    workflow.write_text(
        "[workflow-state:in_progress-inline]\n"
        f"{LEGACY_WORKFLOW_RULE}\n",
        encoding="utf-8",
    )
    hook = root / ".codex" / "hooks" / "inject-workflow-state.py"
    hook.parent.mkdir(parents=True, exist_ok=True)
    hook.write_text(
        "def codex_mode_banner():\n"
        f"    return 'inline: the main session implements/checks directly; {LEGACY_CODEX_MODE_RULE}'\n",
        encoding="utf-8",
    )


def write_external_worktree_override(root: Path, feature_name: str, external: Path) -> None:
    unit = root / ".codestable" / "features" / feature_name
    unit.mkdir(parents=True, exist_ok=True)
    (unit / "worktree-override.md").write_text(
        "---\n"
        f"feature: {feature_name}\n"
        "---\n"
        f"- CLI code lives at `{external}`.\n"
        f"- Approved implementation path: {external}\n",
        encoding="utf-8",
    )


def init_clean_git_repo(path: Path) -> None:
    path.mkdir(parents=True, exist_ok=True)
    readme = path / "README.md"
    if not readme.exists():
        readme.write_text("# tool\n", encoding="utf-8")
    run_git_command(path, "init")
    run_git_command(path, "add", ".")
    run_git_command(path, "-c", "user.name=Vibe Test", "-c", "user.email=vibe@example.invalid", "commit", "-m", "init")


def run_git_command(cwd: Path, *args: str) -> None:
    proc = subprocess.run(
        ["git", *args],
        cwd=str(cwd),
        text=True,
        encoding="utf-8",
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        raise AssertionError(f"git failed: {' '.join(args)}\n{proc.stdout}\n{proc.stderr}")


if __name__ == "__main__":
    unittest.main()
