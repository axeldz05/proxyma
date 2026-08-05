---
name: semantic-compression
description: Write maintainable code by applying semantic compression. Avoid premature abstraction; extract reusable parts only after two or more identical use cases appear. Start by making code usable, then refactor bottom-up to remove duplication.
---

# Semantic Compression

## Core Philosophy
Treat your code like a dictionary compressor that runs continuously. Write concrete, working code first. Compress (refactor) only when duplication emerges naturally. Let abstractions arise from real examples, not upfront design.

## Rules

1. **Write specific code first** – implement exactly what each case needs; ignore “reusability”.
2. **Wait for the second occurrence** – only extract common logic when you see it again (or have a very similar second case).
3. **Compress by pulling out shared parts**:
   - Group repeatedly used local variables into a struct (*shared stack frame*).
   - Wrap repeated blocks into a plain function.
   - Turn functions into methods if they operate on the same struct.
4. **Postpone or eliminate fragile calculations** – e.g., compute total height after building rows rather than pre-counting.
5. **Keep the call site readable** – the final API should read like a step‑by‑step recipe, minimising noise.

## Typical Process (Example)
From repetitive inline layout code:
```c
float x0 = x, y0 = y;
y0 -= row_height; draw_button(x0, y0, w, h, "Auto Snap");
y0 -= row_height; draw_button(x0, y0, w, h, "Reset");
```
1. Create a struct holding at_x, at_y, row_height, etc.
2. Add functions row() and push_button(text).
3. Calculate final panel height only at the end (complete()).

Result:
```c
Panel_Layout layout(this, x, y, width);
layout.window_title(title);

layout.row();
if (layout.push_button("Auto Snap")) { do_auto_snap(this); }

layout.row();
if (layout.push_button("Reset Orientation")) { … }

layout.complete(this);
```
## What to Avoid
* No class hierarchies based on domain nouns (Employee, Manager) before writing code.
* No deep inheritance, templates, or patterns introduced before duplication exists.
* No abstraction from a single use‑case – at least two real examples are required.

## Quality Indicators
* High semantic density: each line expresses a clear domain action.
* Changes are local: altering one behaviour means touching one place.
* Adding a new variant follows the same simple pattern.
* Debugging is straightforward because shared code is a single point of truth.

**One‑line Mantra:**
First make it work, then make it reusable – but only after you’ve seen the same pattern twice.