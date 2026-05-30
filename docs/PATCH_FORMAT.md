# Anchor Patch Format

Models should edit files with compact JSON anchor patches:

```json
{
  "files": [
    {
      "path": "relative/or/absolute/file",
      "expected_sha256": "optional current file hash",
      "hunks": [
        {
          "old": "unique block to replace",
          "new": "replacement block",
          "anchor_before": "optional nearby text before old",
          "anchor_after": "optional nearby text after old"
        }
      ]
    }
  ],
  "verify_commands": ["optional command after apply"],
  "format": true
}
```

Rules:

- Keep hunks small and anchored to unique text.
- Use `anchor_before` or `anchor_after` when `old` may appear more than once.
- Prefer patches over full-file rewrites.
- Verification output is filtered before it returns to the model.
- Multi-file edits are atomic; a failed hunk or verifier rolls all files back.
