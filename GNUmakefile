# GNU make loads this file before Makefile. Keep the existing Makefile as the
# implementation source of truth and expose one stable entry point to agents.
include Makefile

.PHONY: agent-gate

# Symphony, Codex, Claude Code, and CI use the same deterministic completion
# contract. Extend before-commit instead of adding checks only to this alias.
agent-gate: before-commit
