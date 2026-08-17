# G3 docs-example conformance canary (bd-ws0-guards-klz98.4)

This fixture is scanned ONLY by TestDocsExamplesCanary in
tests/docs_conformance_test.go. The gate must flag the two bogus examples
below and must NOT flag the valid one — otherwise the gate is a placebo and
the test fails hard.

A bogus flag the real `ntm` root does not define:

```bash
ntm --no-such-flag
```

A bogus subcommand:

```bash
ntm definitely-not-a-command --json
```

A valid example that must not be flagged:

```bash
ntm version --short
```

A skip-marked example (exactly one — the canary also proves skip counting):

<!-- ntm-docs: skip -->
```bash
ntm robot --this-is-waived-not-checked
```
