#!/usr/bin/env bash
# Reads a pre-push log on stdin. Exit 0 only if it contains at least one failure record and every
# failure is a known flake; exit 1 otherwise (no failures, or any unknown failure).
# Failure records: `--- FAIL: <Test>[/sub]` (any indent) and `FAIL<TAB><package>` lines.
# Passing mentions of a flaky test never count. Test failures are attributed to the next
# `FAIL<TAB>pkg` line: allowed package => explained; otherwise each test must be allowlisted, and a
# package line explained by only-allowlisted tests is not a separate failure.
# Allowlist: the two e2e tests below, and any failure inside internal/transform or internal/runner.
awk '
BEGIN { n = 0; bad = 0; seen = 0 }
/^[ \t]*--- FAIL: / {
  name = $3; sub(/\/.*/, "", name); seen = 1
  pend[++n] = name; next
}
/^FAIL\t/ {
  seen = 1; pkg = $2
  if (pkg ~ /(^|\/)internal\/(transform|runner)(\/|$)/) { n = 0; delete pend; next }
  if (n == 0) bad = 1
  n = 0; for (i in pend) { if (!ok(pend[i])) bad = 1 } delete pend; next
}
function ok(t) {
  return t == "TestScenario14aPausedStopsSensorsAndQueuesRuns" || t == "TestScenario15RestartInterruptsRunsKeepsProposals"
}
END {
  for (i in pend) if (!ok(pend[i])) bad = 1
  exit (seen && !bad) ? 0 : 1
}'
