A ticket, as `propose_tickets` takes it:

```json
{
  "title": "Lock account after failed attempts",
  "kind": "feature",
  "part": "Sign-in",
  "source": {"file": "plan/original.md", "heading": "Parts > Sign-in", "quote": "A stolen password must not be usable without limit."},
  "why": "The plan says a stolen password must not be tried without limit.",
  "what": "Count failed sign-ins per account and lock the account for 15 minutes after five. Telling the owner is not included.",
  "goal": "Five failed sign-ins lock the account for 15 minutes.",
  "checks": [{"kind": "cmd", "spec": "pytest tests/auth/test_lockout.py"}],
  "dependsOn": ["@1"],
  "priority": 2
}
```

`priority` is 0 urgent, 1 high, 2 normal, or 3 low. `dependsOn` names tickets by their number, such as `SF-4`, or `@n` for the n-th ticket of the same call.
