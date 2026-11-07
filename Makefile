# Local checks. AGENTS.md says which target a change needs.
.PHONY: check verify

check:
	python3 -B tools/check_project.py
	python3 -B -m unittest tests/test_claude_review.py

# The full runner. TASK_WORK is a new directory outside the checkout.
verify:
	@test -n "$(TASK_WORK)" || { echo "Set TASK_WORK to a new directory outside the checkout." >&2; exit 2; }
	python3 -B verify.py --work "$(TASK_WORK)"
