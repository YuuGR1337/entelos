# Diagrams

## Decision path

![Decision path from source to replay or restatement](decision-path.svg)

The diagram shows the complete path from [CONSTITUTION.md](../../CONSTITUTION.md): source, proposal, qualification, approval, dated activation, preparation, execution, feedback or correction, replay or restatement.

- The center column names each stage and its operation in [host.go](../../src/lifecycle-core/host.go).
- The dashed boxes on the left show what the application supplies.
- The shaded boxes on the right show what the reference records and checks. [CONTRACTS.md](../CONTRACTS.md) describes these contracts.
- The dashed arrow shows that a fix makes a new revision, so the path starts again at source.

The diagram does not show `suspend`, `describe`, `resolve`, `inspect` or the correction task operations `claim` and `takeover`.

## Files

| File | Use |
|---|---|
| `decision_path.py` | Editable source. It holds the rows and the layout. |
| `decision-path.svg` | Output. It has no external fonts, images or scripts. A CSS media query selects the dark colors. |

The colors come from [docs/brand/](../brand/README.md). If IBM Plex is not installed, the viewer uses its system sans-serif and monospace fonts.

## Change the diagram

1. Edit the rows or the layout in `decision_path.py`.
2. Write the SVG:

   ```sh
   python3 -B docs/diagrams/decision_path.py
   ```

3. Make sure that the SVG agrees with the source:

   ```sh
   python3 -B docs/diagrams/decision_path.py --check
   ```

4. Open the SVG in a browser with the light and the dark color scheme.
