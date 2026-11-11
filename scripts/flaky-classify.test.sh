#!/usr/bin/env bash
# Fixture tests for scripts/flaky-classify.sh. Run by scripts/checks.sh.
set -uo pipefail
cls=$(cd "$(dirname "$0")" && pwd)/flaky-classify.sh
rc=0
t() { # name expected(retry|no) fixture
  if printf '%b' "$3" | "$cls"; then got=retry; else got=no; fi
  [ "$got" = "$2" ] || { echo "FAIL $1: want $2 got $got"; rc=1; }
}
t only-flaky-failing retry '--- PASS: TestA (0.1s)\n--- FAIL: TestScenario14aPausedStopsSensorsAndQueuesRuns (1s)\nFAIL\n'
t flaky-sub-failing retry '    --- FAIL: TestScenario15RestartInterruptsRunsKeepsProposals/x (1s)\n--- FAIL: TestScenario15RestartInterruptsRunsKeepsProposals (1s)\nFAIL\n'
t flaky-passing-unrelated-failing no '--- PASS: TestScenario14aPausedStopsSensorsAndQueuesRuns (1s)\n--- FAIL: TestOther (1s)\nFAIL\n'
t flaky-and-unrelated-failing no '--- FAIL: TestScenario15RestartInterruptsRunsKeepsProposals (1s)\n--- FAIL: TestOther (1s)\nFAIL\n'
t no-fail-lines no 'golangci-lint: 3 issues\n--- PASS: TestScenario14aPausedStopsSensorsAndQueuesRuns\n'
t transform-pkg retry '--- FAIL: TestWhatever (1s)\nFAIL\nFAIL\thal9001/internal/transform\t0.5s\n'
t runner-and-other-pkg no 'FAIL\thal9001/internal/runner\t1s\nFAIL\thal9001/internal/store\t1s\n'
t other-pkg-only no 'FAIL\thal9001/internal/store [build failed]\n'
[ $rc = 0 ] && echo "flaky-classify tests passed"
exit $rc
