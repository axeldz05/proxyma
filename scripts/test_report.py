#!/usr/bin/env python3
"""
Proxyma Unified Test Runner & Failure Reporter.

Executes tests and produces a clear, actionable summary of failures including:
  - Exact file path
  - Test name
  - Line of code
  - Failure message and trace

Supports Go tests, E2E tests, and Android tests.
Saves detailed failure reports containing full traces for failing tests only
(omitting passed tests except for the summary counter).
"""

from __future__ import annotations

from dataclasses import dataclass, field
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
from typing import Dict, List, Optional, Tuple
import xml.etree.ElementTree as ET

# ANSI Colors
RED = "\033[0;31m"
GREEN = "\033[0;32m"
YELLOW = "\033[0;33m"
BLUE = "\033[0;34m"
MAGENTA = "\033[0;35m"
CYAN = "\033[0;36m"
BOLD = "\033[1m"
NC = "\033[0m"


def colorize(text: str, color: str) -> str:
    if not sys.stdout.isatty() and not os.environ.get("FORCE_COLOR"):
        return text
    return f"{color}{text}{NC}"


def strip_ansi(text: str) -> str:
    """Removes ANSI color and cursor control sequences from a string."""
    return re.sub(r"\x1b\[[0-9;]*[a-zA-Z]", "", text)


@dataclass
class TestFailure:
    suite: str
    package: str
    test_name: str
    file_path: str
    line: int
    reason: str
    details: List[str] = field(default_factory=list)
    full_trace: List[str] = field(default_factory=list)

    def display(self, index: Optional[int] = None, full_trace: bool = False) -> str:
        idx_prefix = f"[{index}] " if index is not None else ""
        header = f"{idx_prefix}{self.file_path}:{self.line}"
        lines = [
            colorize(header, RED + BOLD),
            f"   {colorize('Suite:', CYAN)}   {self.suite}",
            f"   {colorize('Test:', BOLD)}    {self.test_name}",
        ]
        if self.package and self.package != self.test_name:
            lines.append(f"   {colorize('Package:', CYAN)} {self.package}")
        if self.reason:
            clean_reason = self.reason.strip()
            indented = "\n             ".join(clean_reason.splitlines())
            lines.append(f"   {colorize('Reason:', YELLOW)} {indented}")

        trace_to_show = self.full_trace if full_trace else self.details[:4]
        if trace_to_show:
            trace_label = "Full Trace:" if full_trace else "Trace:"
            lines.append(f"   {colorize(trace_label, MAGENTA)}")
            for d in trace_to_show:
                lines.append(f"     {d}")
        return "\n".join(lines)

    def to_detailed_string(self, index: Optional[int] = None, total: Optional[int] = None) -> str:
        count_str = (
            f"[{index}/{total}] "
            if index is not None and total is not None
            else (f"[{index}] " if index is not None else "")
        )
        header = f"{count_str}{self.file_path}:{self.line}"
        lines = [
            header,
            f"   Suite:       {self.suite}",
            f"   Test:        {self.test_name}",
        ]
        if self.package and self.package != self.test_name:
            lines.append(f"   Package:     {self.package}")
        if self.reason:
            clean_reason = strip_ansi(self.reason.strip())
            indented = "\n                ".join(clean_reason.splitlines())
            lines.append(f"   Reason:      {indented}")

        lines.append("   Full Trace:")
        trace = self.full_trace if self.full_trace else self.details
        if trace:
            for d in trace:
                lines.append(f"     {strip_ansi(d)}")
        else:
            lines.append("     (No trace captured)")
        return "\n".join(lines)


class GoTestParser:
    """Parses `go test -json` streaming output and standard text output."""

    def __init__(self, repo_root: Path, verbose: bool = False):
        self.repo_root = repo_root.resolve()
        self.verbose = verbose

        self.pkg_outputs: Dict[str, List[str]] = {}
        self.test_outputs: Dict[str, Dict[str, List[str]]] = {}
        self.test_statuses: Dict[str, Dict[str, str]] = {}

        self.passed_count = 0
        self.failed_count = 0
        self.skipped_count = 0
        self.failures: List[TestFailure] = []

    def process_json_line(self, raw_line: str) -> None:
        raw_line = raw_line.strip()
        if not raw_line:
            return

        try:
            event = json.loads(raw_line)
        except json.JSONDecodeError:
            if self.verbose:
                print(raw_line)
            return

        action = event.get("Action")
        pkg = event.get("Package", "")
        test = event.get("Test")
        output = event.get("Output", "")

        if pkg not in self.pkg_outputs:
            self.pkg_outputs[pkg] = []
            self.test_outputs[pkg] = {}
            self.test_statuses[pkg] = {}

        if test:
            if test not in self.test_outputs[pkg]:
                self.test_outputs[pkg][test] = []
            if output:
                self.test_outputs[pkg][test].append(output)
        elif output:
            self.pkg_outputs[pkg].append(output)

        if self.verbose and output:
            print(output, end="")

        if action == "pass" and test:
            self.passed_count += 1
            self.test_statuses[pkg][test] = "pass"
            if not self.verbose:
                sys.stdout.write(f"\r{colorize('✓', GREEN)} {pkg}: {test} ({event.get('Elapsed', 0):.2f}s)   \033[K")
                sys.stdout.flush()

        elif action == "skip" and test:
            self.skipped_count += 1
            self.test_statuses[pkg][test] = "skip"

        elif action == "fail" and test:
            self.failed_count += 1
            self.test_statuses[pkg][test] = "fail"
            test_lines = self.test_outputs[pkg].get(test, [])
            failure = self._extract_failure(pkg, test, test_lines)
            self.failures.append(failure)
            if not self.verbose:
                sys.stdout.write(f"\r{colorize('✗ [FAIL]', RED + BOLD)} {failure.file_path}:{failure.line} in {test}\n")
                sys.stdout.flush()

        elif action == "fail" and not test:
            has_test_failures = any(f.package == pkg for f in self.failures)
            if not has_test_failures:
                pkg_lines = self.pkg_outputs.get(pkg, [])
                failure = self._extract_package_failure(pkg, pkg_lines)
                self.failed_count += 1
                self.failures.append(failure)
                if not self.verbose:
                    sys.stdout.write(f"\r{colorize('✗ [FAIL]', RED + BOLD)} {failure.file_path}:{failure.line} ({pkg})\n")
                    sys.stdout.flush()

    def _extract_failure(self, pkg: str, test: str, lines: List[str]) -> TestFailure:
        file_path = ""
        line_no = 0
        reason_lines: List[str] = []
        detail_lines: List[str] = []

        # 1. Error Trace: /path/to/file_test.go:98
        trace_matches = []
        for line in lines:
            m = re.search(r"Error Trace:\s+([^\s:]+):(\d+)", line)
            if m:
                path_str = m.group(1)
                lno = int(m.group(2))
                if not ("/src/testing/" in path_str or "testing.go" in path_str):
                    trace_matches.append((path_str, lno))

        if trace_matches:
            file_path, line_no = trace_matches[0]

        # 2. Standard go test failure: foo_test.go:123: message
        if not file_path:
            for line in lines:
                m = re.search(r"^\s*([a-zA-Z0-9_\-\./]+\.go):(\d+):(?:\s*(.*))?$", line)
                if m:
                    path_str = m.group(1)
                    lno = int(m.group(2))
                    if not ("testing.go" in path_str):
                        file_path = path_str
                        line_no = lno
                        if m.group(3):
                            reason_lines.append(m.group(3))
                        break

        # 3. Panic trace
        if not file_path:
            for line in lines:
                m = re.search(r"^\s*([a-zA-Z0-9_\-\./]+\.go):(\d+)(?:\s+\+0x[0-9a-fA-F]+)?$", line)
                if m:
                    path_str = m.group(1)
                    lno = int(m.group(2))
                    if not ("/src/" in path_str and "proxyma" not in path_str):
                        file_path = path_str
                        line_no = lno
                        break

        # 4. Extract reason
        for line in lines:
            clean = line.strip()
            if clean.startswith("Error:") or clean.startswith("Messages:"):
                reason_lines.append(clean)
            elif "panic:" in clean:
                reason_lines.append(clean)
            elif re.search(r"(?:Should|Expected|want|got|contract failed|StopNodeWithError)", clean, re.IGNORECASE):
                if clean not in reason_lines and len(reason_lines) < 3:
                    reason_lines.append(clean)

        for line in lines:
            clean = line.strip()
            if clean and not clean.startswith("=== ") and not clean.startswith("--- "):
                if clean not in reason_lines and len(detail_lines) < 6:
                    detail_lines.append(clean)

        file_path = self._normalize_path(pkg, file_path, test)
        reason = " | ".join(reason_lines) if reason_lines else (detail_lines[0] if detail_lines else "Test failed")

        # Collect full unabridged trace including subtests if any
        full_trace: List[str] = []
        all_lines = list(lines)
        for t_name, t_lines in self.test_outputs.get(pkg, {}).items():
            if t_name != test and t_name.startswith(test + "/"):
                all_lines.extend(t_lines)

        for raw_line in all_lines:
            for split_l in raw_line.splitlines():
                clean_l = split_l.rstrip("\r\n")
                if clean_l:
                    full_trace.append(clean_l)

        return TestFailure(
            suite="go",
            package=pkg,
            test_name=test,
            file_path=file_path,
            line=line_no,
            reason=reason,
            details=detail_lines,
            full_trace=full_trace,
        )

    def _extract_package_failure(self, pkg: str, lines: List[str]) -> TestFailure:
        file_path = ""
        line_no = 0
        reasons = []

        for line in lines:
            m = re.search(r"^([a-zA-Z0-9_\-\./]+\.go):(\d+):(?:\d+:)?\s*(.*)$", line.strip())
            if m:
                file_path = m.group(1)
                line_no = int(m.group(2))
                reasons.append(m.group(3))
                break

        file_path = self._normalize_path(pkg, file_path, "")
        reason = reasons[0] if reasons else "Package compilation or initialization failed"

        full_trace: List[str] = []
        for raw_line in lines:
            for split_l in raw_line.splitlines():
                clean_l = split_l.rstrip("\r\n")
                if clean_l:
                    full_trace.append(clean_l)

        return TestFailure(
            suite="go",
            package=pkg,
            test_name=pkg,
            file_path=file_path,
            line=line_no,
            reason=reason,
            details=[l.strip() for l in lines if l.strip()][:5],
            full_trace=full_trace,
        )

    def _normalize_path(self, pkg: str, file_path: str, test: str) -> str:
        clean_pkg = pkg.replace("proxyma/", "")
        if not file_path:
            # Try to search for test name in repo
            if test:
                found = self._find_test_file_in_pkg(clean_pkg, test)
                if found:
                    return found
            return clean_pkg if clean_pkg else "unknown"

        path_obj = Path(file_path)
        if path_obj.is_absolute():
            try:
                return str(path_obj.relative_to(self.repo_root))
            except ValueError:
                return file_path

        # If it's a bare filename like fixtures_test.go
        if "/" not in file_path:
            if clean_pkg:
                cand = self.repo_root / clean_pkg / file_path
                if cand.exists():
                    return f"{clean_pkg}/{file_path}"
            matches = list(self.repo_root.glob(f"**/{file_path}"))
            if matches:
                try:
                    return str(matches[0].relative_to(self.repo_root))
                except ValueError:
                    pass

        return file_path

    def _find_test_file_in_pkg(self, pkg: str, test_name: str) -> Optional[str]:
        base_test = test_name.split("/")[0]
        search_dir = self.repo_root / pkg if pkg else self.repo_root
        if not search_dir.exists():
            return None
        for p in search_dir.glob("*_test.go"):
            try:
                content = p.read_text(errors="replace")
                if f"func {base_test}(" in content:
                    return str(p.relative_to(self.repo_root))
            except Exception:
                pass
        return None


class E2ETestParser:
    """Parses E2E test runner outputs and logs."""

    def __init__(self, repo_root: Path):
        self.repo_root = repo_root.resolve()
        self.failures: List[TestFailure] = []

    def parse_log_file(self, case_name: str, log_file: Path) -> TestFailure:
        case_script = self.repo_root / "tests" / "e2e" / "cases" / f"{case_name}.sh"
        file_path = f"tests/e2e/cases/{case_name}.sh"
        line_no = 0
        reasons: List[str] = []
        details: List[str] = []

        if not log_file.exists():
            # Check under tests/e2e/logs/failed/*/<case_name>.log
            matches = list(self.repo_root.glob(f"tests/e2e/logs/failed/*/{case_name}.log"))
            if matches:
                log_file = matches[-1]

        if not log_file.exists():
            return TestFailure(
                suite="e2e",
                package="tests/e2e",
                test_name=case_name,
                file_path=file_path,
                line=line_no,
                reason="Log file missing",
            )

        content = log_file.read_text(errors="replace")
        lines = content.splitlines()

        for line in lines:
            m = re.search(r"\[FAIL_LOC\]\s+([^:]+):(\d+)(?::\s*(.*))?", line)
            if m:
                path_str = m.group(1)
                lno = int(m.group(2))
                try:
                    rel = str(Path(path_str).resolve().relative_to(self.repo_root))
                    file_path = rel
                except ValueError:
                    file_path = path_str
                line_no = lno
                if m.group(3):
                    reasons.append(m.group(3))
                break

        for line in lines:
            if "❌" in line:
                reasons.append(line.replace("❌", "").strip())
            m = re.search(r"at\s+([^:]+):(\d+)", line)
            if m and line_no == 0:
                try:
                    file_path = str(Path(m.group(1)).resolve().relative_to(self.repo_root))
                except ValueError:
                    file_path = m.group(1)
                line_no = int(m.group(2))

        if line_no == 0 and case_script.exists():
            # Look for last command before error dump
            for line in reversed(lines):
                if "command failed" in line:
                    reasons.append(line.strip())
                    break

        details = [l.strip() for l in lines[-15:] if l.strip()]
        reason = " | ".join(reasons) if reasons else "E2E case failed"
        full_trace = [l.rstrip("\r\n") for l in lines]

        return TestFailure(
            suite="e2e",
            package="tests/e2e",
            test_name=case_name,
            file_path=file_path,
            line=line_no,
            reason=reason,
            details=details[:5],
            full_trace=full_trace,
        )


class AndroidTestParser:
    """Parses Android Gradle test results and verification logs."""

    def __init__(self, repo_root: Path):
        self.repo_root = repo_root.resolve()
        self.failures: List[TestFailure] = []

    def parse_junit_xml_dir(self, test_results_dir: Path) -> List[TestFailure]:
        failures: List[TestFailure] = []
        if not test_results_dir.exists():
            return failures

        for xml_file in test_results_dir.glob("**/TEST-*.xml"):
            try:
                tree = ET.parse(xml_file)
                root = tree.getroot()
                for testcase in root.findall(".//testcase"):
                    failure = testcase.find("failure")
                    error = testcase.find("error")
                    err_node = failure if failure is not None else error
                    if err_node is None:
                        continue

                    test_name = testcase.get("name", "unknown")
                    class_name = testcase.get("classname", "unknown")
                    msg = err_node.get("message", "")
                    trace_text = err_node.text or ""

                    file_path, line_no = self._resolve_android_trace(class_name, trace_text)
                    full_trace = [l.rstrip("\r\n") for l in trace_text.splitlines()]

                    failures.append(
                        TestFailure(
                            suite="android",
                            package=class_name,
                            test_name=f"{class_name}.{test_name}",
                            file_path=file_path,
                            line=line_no,
                            reason=msg or "Assertion error",
                            details=[l.strip() for l in trace_text.splitlines() if l.strip()][:5],
                            full_trace=full_trace,
                        )
                    )
            except Exception as e:
                print(f"Warning: could not parse JUnit XML {xml_file}: {e}", file=sys.stderr)

        return failures

    def parse_gradle_output(self, output: str) -> List[TestFailure]:
        failures: List[TestFailure] = []
        m = re.search(r"Execution failed for task '([^']+)'.*?> (.*)", output, re.DOTALL)
        if m:
            task = m.group(1)
            reason = m.group(2).splitlines()[0].strip()

            file_path = "cmd/proxyma-android/app/build.gradle.kts"
            line_no = 0
            fl_match = re.search(r"build\.gradle\.kts:(\d+)", output)
            if fl_match:
                line_no = int(fl_match.group(1))

            full_trace = [l.rstrip("\r\n") for l in output.splitlines()]
            failures.append(
                TestFailure(
                    suite="android",
                    package="gradle",
                    test_name=task,
                    file_path=file_path,
                    line=line_no,
                    reason=reason,
                    details=[l.strip() for l in output.splitlines() if "FAILED" in l or "Error" in l][:4],
                    full_trace=full_trace,
                )
            )
        return failures

    def _resolve_android_trace(self, class_name: str, trace_text: str) -> tuple[str, int]:
        file_path = "cmd/proxyma-android"
        line_no = 0

        matches = re.findall(
            r"at\s+([a-zA-Z0-9_$.]+)\.([a-zA-Z0-9_$]+)\(([a-zA-Z0-9_]+\.kt|[a-zA-Z0-9_]+\.java):(\d+)\)",
            trace_text,
        )
        for pkg_class, method, file_name, line_str in matches:
            if "com.proxyma" in pkg_class or "contracts" in pkg_class:
                found = list(self.repo_root.glob(f"cmd/proxyma-android/**/{file_name}"))
                if found:
                    try:
                        file_path = str(found[0].relative_to(self.repo_root))
                    except ValueError:
                        file_path = str(found[0])
                else:
                    file_path = f"cmd/proxyma-android/app/src/test/java/{file_name}"
                line_no = int(line_str)
                break

        return file_path, line_no


def write_detailed_failures_file(
    failures: List[TestFailure],
    passed: int,
    skipped: int,
    elapsed: float,
    output_path: Path,
) -> None:
    """
    Saves a clean, unabridged failures report to output_path.
    Only failing tests with their full traces are included in the body;
    passed tests are omitted except for the summary counter.
    """
    total_fails = len(failures)
    total_tests = passed + total_fails + skipped
    now_str = time.strftime("%Y-%m-%d %H:%M:%S")

    content_lines = [
        "=" * 80,
        "PROXYMA TEST FAILURES REPORT (DETAILED FULL TRACE)",
        f"Generated: {now_str}",
        f"Summary:   {passed} passed, {total_fails} failed, {skipped} skipped ({total_tests} total) in {elapsed:.2f}s",
        "=" * 80,
        "",
    ]

    if total_fails == 0:
        content_lines.append("All tests passed! No failing tests to report.")
        content_lines.append("")
    else:
        content_lines.append(f"Showing full traces for {total_fails} failing test(s) only:")
        content_lines.append("(Passed tests are omitted from this report; see summary above for total counts)")
        content_lines.append("")
        content_lines.append("-" * 80)
        content_lines.append("")

        for idx, failure in enumerate(failures, start=1):
            content_lines.append(failure.to_detailed_string(idx, total_fails))
            content_lines.append("")
            content_lines.append("-" * 80)
            content_lines.append("")

        content_lines.append("=" * 80)
        content_lines.append(f"END OF REPORT: {passed} passed, {total_fails} failed, {skipped} skipped")
        content_lines.append("=" * 80)
        content_lines.append("")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text("\n".join(content_lines), encoding="utf-8")


def print_summary(
    failures: List[TestFailure],
    passed: int,
    skipped: int,
    elapsed: float,
    output_path: Optional[Path] = None,
    full_trace_stdout: bool = False,
) -> int:
    total_fails = len(failures)
    print("\n" + "=" * 72)
    if total_fails == 0:
        print(colorize(f"✅ ALL TESTS PASSED: {passed} passed, {skipped} skipped in {elapsed:.2f}s", GREEN + BOLD))
        print("=" * 72)
        if output_path:
            write_detailed_failures_file(failures, passed, skipped, elapsed, output_path)
            if output_path.exists():
                print(f"Report updated: {output_path}")
        return 0

    print(colorize(f"❌ FAILED TESTS SUMMARY ({total_fails} failure{'s' if total_fails != 1 else ''})", RED + BOLD))
    print("=" * 72)
    for idx, f in enumerate(failures, start=1):
        print(f.display(idx, full_trace=full_trace_stdout))
        if idx < total_fails:
            print("-" * 72)

    print("=" * 72)
    summary_str = f"Total: {passed} passed, {total_fails} failed, {skipped} skipped in {elapsed:.2f}s"
    print(colorize(summary_str, RED + BOLD))

    if output_path:
        write_detailed_failures_file(failures, passed, skipped, elapsed, output_path)
        print(colorize(f"📄 Detailed failure report with full traces saved to: {output_path}", CYAN + BOLD))

    print("=" * 72)
    return 1


def run_go_tests(
    repo_root: Path,
    extra_args: List[str],
    output_path: Optional[Path] = None,
    verbose: bool = False,
    full_trace_stdout: bool = False,
) -> int:
    target_display = " ".join(extra_args) if extra_args else "./..."
    print(colorize(f"▶ Running Go tests: {target_display}", BLUE + BOLD))
    t0 = time.time()

    has_target = any(not a.startswith("-") for a in extra_args)
    cmd = ["go", "test", "-json"]
    if not has_target:
        cmd.append("./...")
    cmd.extend(extra_args)

    parser = GoTestParser(repo_root, verbose=verbose)
    proc = subprocess.Popen(
        cmd,
        cwd=repo_root,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        bufsize=1,
    )

    if proc.stdout:
        for line in proc.stdout:
            parser.process_json_line(line)

    proc.wait()
    elapsed = time.time() - t0
    sys.stdout.write("\r\033[K")
    sys.stdout.flush()

    return print_summary(
        parser.failures,
        parser.passed_count,
        parser.skipped_count,
        elapsed,
        output_path=output_path,
        full_trace_stdout=full_trace_stdout,
    )


def run_e2e_tests(
    repo_root: Path,
    extra_args: List[str],
    output_path: Optional[Path] = None,
    full_trace_stdout: bool = False,
) -> int:
    print(colorize("▶ Running E2E tests...", BLUE + BOLD))
    t0 = time.time()

    env = os.environ.copy()
    runner = repo_root / "tests" / "e2e" / "run.sh"
    cmd = [str(runner)] + extra_args

    proc = subprocess.Popen(
        cmd,
        cwd=repo_root,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env=env,
    )

    failed_case_names = []
    passed_cases = 0

    if proc.stdout:
        for line in proc.stdout:
            sys.stdout.write(line)
            sys.stdout.flush()
            if "🟢 [PASS]" in line:
                passed_cases += 1
            m = re.search(r"🔴 \[FAIL\]\s+([^\s]+)", line)
            if m:
                failed_case_names.append(m.group(1))

    proc.wait()
    elapsed = time.time() - t0

    failures = []
    e2e_parser = E2ETestParser(repo_root)
    log_dir = repo_root / "tests" / "e2e" / "logs"

    for case_name in failed_case_names:
        log_file = log_dir / f"{case_name}.log"
        failures.append(e2e_parser.parse_log_file(case_name, log_file))

    return print_summary(
        failures,
        passed_cases,
        0,
        elapsed,
        output_path=output_path,
        full_trace_stdout=full_trace_stdout,
    )


def run_android_tests(
    repo_root: Path,
    extra_args: List[str],
    output_path: Optional[Path] = None,
    full_trace_stdout: bool = False,
) -> int:
    print(colorize("▶ Running Android contract tests...", BLUE + BOLD))
    t0 = time.time()

    failures: List[TestFailure] = []
    passed = 0

    # 1. Host contract Go tests
    go_cmd = [
        "go",
        "test",
        "-json",
        "-count=1",
        "-tags=androidcontract",
        "./internal/compute/androidcontract",
        "./internal/server/androidcontract",
    ]
    parser = GoTestParser(repo_root, verbose=False)
    p = subprocess.Popen(go_cmd, cwd=repo_root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if p.stdout:
        for line in p.stdout:
            parser.process_json_line(line)
    p.wait()
    failures.extend(parser.failures)
    passed += parser.passed_count

    # 2. Check for JUnit XML results
    junit_dir = repo_root / "cmd" / "proxyma-android" / "app" / "build" / "test-results"
    android_parser = AndroidTestParser(repo_root)
    failures.extend(android_parser.parse_junit_xml_dir(junit_dir))

    elapsed = time.time() - t0
    return print_summary(
        failures,
        passed,
        parser.skipped_count,
        elapsed,
        output_path=output_path,
        full_trace_stdout=full_trace_stdout,
    )


def run_all_tests(
    repo_root: Path,
    extra_args: List[str],
    output_path: Optional[Path] = None,
    verbose: bool = False,
    full_trace_stdout: bool = False,
) -> int:
    t0 = time.time()
    all_failures: List[TestFailure] = []
    total_passed = 0
    total_skipped = 0

    # 1. Go tests
    print(colorize("▶ [1/3] Running Go test suite...", BLUE + BOLD))
    p_go = GoTestParser(repo_root, verbose=verbose)
    go_cmd = ["go", "test", "-json", "./..."] + extra_args
    proc = subprocess.Popen(go_cmd, cwd=repo_root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
    if proc.stdout:
        for line in proc.stdout:
            p_go.process_json_line(line)
    proc.wait()
    all_failures.extend(p_go.failures)
    total_passed += p_go.passed_count
    total_skipped += p_go.skipped_count

    # 2. Android contract tests
    print(colorize("\n▶ [2/3] Running Android contract tests...", BLUE + BOLD))
    p_and = GoTestParser(repo_root, verbose=False)
    and_cmd = [
        "go",
        "test",
        "-json",
        "-count=1",
        "-tags=androidcontract",
        "./internal/compute/androidcontract",
        "./internal/server/androidcontract",
    ]
    proc = subprocess.Popen(and_cmd, cwd=repo_root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    if proc.stdout:
        for line in proc.stdout:
            p_and.process_json_line(line)
    proc.wait()
    all_failures.extend(p_and.failures)
    total_passed += p_and.passed_count
    total_skipped += p_and.skipped_count

    junit_dir = repo_root / "cmd" / "proxyma-android" / "app" / "build" / "test-results"
    android_parser = AndroidTestParser(repo_root)
    all_failures.extend(android_parser.parse_junit_xml_dir(junit_dir))

    # 3. E2E tests
    runner = repo_root / "tests" / "e2e" / "run.sh"
    if runner.exists():
        print(colorize("\n▶ [3/3] Running E2E tests...", BLUE + BOLD))
        env = os.environ.copy()
        proc = subprocess.Popen([str(runner)], cwd=repo_root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=env)
        failed_cases = []
        if proc.stdout:
            for line in proc.stdout:
                sys.stdout.write(line)
                sys.stdout.flush()
                if "🟢 [PASS]" in line:
                    total_passed += 1
                m = re.search(r"🔴 \[FAIL\]\s+([^\s]+)", line)
                if m:
                    failed_cases.append(m.group(1))
        proc.wait()
        e2e_parser = E2ETestParser(repo_root)
        log_dir = repo_root / "tests" / "e2e" / "logs"
        for case_name in failed_cases:
            log_file = log_dir / f"{case_name}.log"
            all_failures.append(e2e_parser.parse_log_file(case_name, log_file))

    elapsed = time.time() - t0
    sys.stdout.write("\r\033[K")
    sys.stdout.flush()

    return print_summary(
        all_failures,
        total_passed,
        total_skipped,
        elapsed,
        output_path=output_path,
        full_trace_stdout=full_trace_stdout,
    )


def parse_text_log(repo_root: Path, content: str) -> Tuple[List[TestFailure], int, int]:
    lines = content.splitlines()
    failures: List[TestFailure] = []
    passed = 0
    skipped = 0

    parser = GoTestParser(repo_root, verbose=False)

    for line in lines:
        m_pkg_pass = re.search(r"ok\s+([^\s]+)\s+([0-9\.]+s|\(cached\))", line)
        if m_pkg_pass:
            passed += 1

    # Check if this log is already a test_report.py summary output
    if "FAILED TESTS SUMMARY" in content or "✗ [FAIL]" in content:
        m_tot = re.search(r"Total:\s+(\d+)\s+passed,\s+(\d+)\s+failed(?:,\s+(\d+)\s+skipped)?", content)
        if m_tot:
            passed = int(m_tot.group(1))
            skipped = int(m_tot.group(3)) if m_tot.group(3) else 0

        # Extract blocks [1] path:line ...
        blocks = re.findall(
            r"\[(\d+)\]\s+([^:\s]+):(\d+)\n\s+Suite:\s+([^\n]+)\n\s+Test:\s+([^\n]+)(?:\n\s+Package:\s+([^\n]+))?(?:\n\s+Reason:\s+([^\n]+))?(?:\n\s+(?:Full Trace|Trace):\n((?:\s{4,}.*\n?)*))?",
            content,
        )
        for b in blocks:
            idx, fpath, lno, suite, tname, pkg, reason, trace_block = b
            trace_lines = [l.strip() for l in (trace_block or "").splitlines() if l.strip()]
            failures.append(
                TestFailure(
                    suite=suite.strip(),
                    package=pkg.strip() if pkg else "",
                    test_name=tname.strip(),
                    file_path=fpath.strip(),
                    line=int(lno),
                    reason=reason.strip() if reason else "Test failed",
                    details=trace_lines[:5],
                    full_trace=trace_lines,
                )
            )
        if failures:
            return failures, passed, skipped

    # Scan for --- FAIL: TestName in standard go test -v output
    for idx, line in enumerate(lines):
        m_fail = re.search(r"--- FAIL:\s+([^\s]+)", line)
        if m_fail:
            test_name = m_fail.group(1)

            # Find matching === RUN   test_name backwards
            start_idx = max(0, idx - 100)
            run_pattern = f"=== RUN   {test_name}"
            for back_idx in range(idx - 1, max(-1, idx - 200), -1):
                if lines[back_idx].strip() == run_pattern:
                    start_idx = back_idx
                    break
            test_trace_lines = lines[start_idx : idx + 1]

            # Try to identify package from nearby lines or repo search
            pkg = ""
            for l in lines[max(0, idx - 10):min(len(lines), idx + 10)]:
                m_p = re.search(r"(?:proxyma/([a-zA-Z0-9_\-\./]+))", l)
                if m_p:
                    pkg = "proxyma/" + m_p.group(1).split()[0]
                    break

            failure = parser._extract_failure(pkg or "unknown", test_name, test_trace_lines)
            failure.full_trace = [l.rstrip("\r\n") for l in test_trace_lines]
            failures.append(failure)

    return failures, passed, skipped


def parse_existing_log(
    repo_root: Path,
    file_path: str,
    suite_type: str = "go",
    output_path: Optional[Path] = None,
    full_trace_stdout: bool = False,
) -> int:
    t0 = time.time()
    failures: List[TestFailure] = []
    passed = 0
    skipped = 0

    if file_path == "-" or not file_path:
        content = sys.stdin.read()
    else:
        content = Path(file_path).read_text(errors="replace")

    lines = content.splitlines()

    if suite_type == "go":
        is_json = False
        for line in lines[:30]:
            clean = line.strip()
            if clean.startswith('{"Time":') or clean.startswith('{"Action":'):
                is_json = True
                break

        if is_json:
            parser = GoTestParser(repo_root, verbose=False)
            for line in lines:
                parser.process_json_line(line)
            failures = parser.failures
            passed = parser.passed_count
            skipped = parser.skipped_count
        else:
            failures, passed, skipped = parse_text_log(repo_root, content)

    elif suite_type == "e2e":
        e2e_parser = E2ETestParser(repo_root)
        for line in lines:
            m = re.search(r"🔴 \[FAIL\]\s+([^\s]+)", line)
            if m:
                case_name = m.group(1)
                log_file = repo_root / "tests" / "e2e" / "logs" / f"{case_name}.log"
                failures.append(e2e_parser.parse_log_file(case_name, log_file))

    elif suite_type == "android":
        android_parser = AndroidTestParser(repo_root)
        failures.extend(android_parser.parse_gradle_output(content))

    return print_summary(
        failures,
        passed,
        skipped,
        time.time() - t0,
        output_path=output_path,
        full_trace_stdout=full_trace_stdout,
    )


def main() -> int:
    repo_root = Path(__file__).resolve().parent.parent

    # Custom argument dispatch to avoid argparse consuming -run, -v, etc.
    args = sys.argv[1:]
    subcommand = "go"
    verbose = False
    full_trace_stdout = False
    explicit_output: Optional[Path] = None
    clean_args: List[str] = []

    # First pass: check for help
    if any(a in ("help", "--help", "-h") for a in args):
        print("Usage: test_report.py [go|e2e|android|all|parse] [options] [test_args...]")
        print("")
        print("Subcommands:")
        print("  go [args...]          Run Go tests (default)")
        print("  e2e [args...]         Run E2E tests")
        print("  android [args...]     Run Android contract tests")
        print("  all [args...]         Run all test suites (Go + Android + E2E)")
        print("  parse <file>          Parse an existing test log or JSON file")
        print("")
        print("Options:")
        print("  -o, --output <file>   Save detailed failures report with full traces to file")
        print("  -f, --full-trace      Display full unabridged traces on terminal stdout as well")
        print("  --verbose             Stream raw test output events in real-time")
        print("")
        print("By default, if any test fails, a detailed failure report with full traces")
        print("is automatically saved to 'test-failures.log' (omitting passed tests).")
        return 0

    # Extract flags: -o, --output, -f, --full-trace, --verbose
    idx = 0
    while idx < len(args):
        arg = args[idx]
        if arg in ("-o", "--output"):
            if idx + 1 < len(args):
                explicit_output = Path(args[idx + 1]).resolve()
                idx += 2
                continue
            else:
                print("Error: -o/--output requires a filename argument", file=sys.stderr)
                return 2
        elif arg.startswith("--output="):
            explicit_output = Path(arg.split("=", 1)[1]).resolve()
            idx += 1
            continue
        elif arg in ("-f", "--full-trace"):
            full_trace_stdout = True
            idx += 1
            continue
        elif arg in ("--verbose",):
            verbose = True
            idx += 1
            continue
        else:
            clean_args.append(arg)
            idx += 1

    if clean_args and clean_args[0] in ("go", "e2e", "android", "all", "parse"):
        subcommand = clean_args[0]
        clean_args = clean_args[1:]

    # Resolve output file: explicit > env var > default "test-failures.log"
    env_output = os.environ.get("TEST_REPORT_OUTPUT")
    if explicit_output:
        output_path: Optional[Path] = explicit_output
    elif env_output:
        output_path = Path(env_output).resolve()
    else:
        output_path = (repo_root / "test-failures.log").resolve()

    if subcommand == "go":
        return run_go_tests(
            repo_root,
            clean_args,
            output_path=output_path,
            verbose=verbose,
            full_trace_stdout=full_trace_stdout,
        )
    elif subcommand == "e2e":
        return run_e2e_tests(
            repo_root,
            clean_args,
            output_path=output_path,
            full_trace_stdout=full_trace_stdout,
        )
    elif subcommand == "android":
        return run_android_tests(
            repo_root,
            clean_args,
            output_path=output_path,
            full_trace_stdout=full_trace_stdout,
        )
    elif subcommand == "all":
        return run_all_tests(
            repo_root,
            clean_args,
            output_path=output_path,
            verbose=verbose,
            full_trace_stdout=full_trace_stdout,
        )
    elif subcommand == "parse":
        target_file = clean_args[0] if clean_args else "-"
        return parse_existing_log(
            repo_root,
            target_file,
            suite_type="go",
            output_path=output_path,
            full_trace_stdout=full_trace_stdout,
        )

    return run_go_tests(
        repo_root,
        clean_args,
        output_path=output_path,
        verbose=verbose,
        full_trace_stdout=full_trace_stdout,
    )


if __name__ == "__main__":
    sys.exit(main())
