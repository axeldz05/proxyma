# Continuous Granularity

**Objective:** To build and modify APIs, abstractions, and user interfaces using a compression-oriented approach. The goal is to maximize code compaction while maintaining continuous granularity—ensuring high-level wrappers never eliminate or obscure lower-level control, avoiding API "holes" or artificial restrictions.

## Core Principles

1. **Compression Over A Priori Design:** Do not pre-design complex object hierarchies, abstractions, or deep architectures. Write the inline, low-level, concrete code first. Compress it into functions or layout wrappers only when redundant patterns emerge.

2. **Total Cost Focus:** Evaluate all code changes strictly by their impact on total development time and maintainability. Accept localized, slightly "ugly" solutions (e.g., passing explicit counts) if the alternative requires highly complex, error-prone language magic (e.g., deep template meta-programming or deferred logic loops).

3. **Continuous Granularity:** Maintain every layer of control. When bundling low-level operations into a high-level function, keep the smaller pieces exposed and functional. High-level functions must always be trivially replaceable by the lower-level components they compose.

## Implementation Rules & Anti-Patterns
### Layering vs. Destructive Modification
When a specific high-level abstraction needs to adapt to a new, hyper-specific pattern (e.g., a toggle checkbox button instead of a plain click button):

* **DO NOT** modify the existing intermediate function to strictly require the new paradigm (e.g., forcing a pointer to a mutable boolean into a generic button call). This creates a granularity discontinuity for users who need the intermediate functionality without the data-binding wrapper.
* **DO** leave the intermediate function intact and build a shallow, higher-level layer on top of it (bool_button() wrapping push_button()).

### The Three-Tier Architecture Rule

Always maintain access to three distinct levels of API granularity:

+-----------------------------------------------------------------------+
| Level 3: High-Level Utility (e.g., layout.bool_button("X", &var))     |
+-----------------------------------------------------------------------+
| Level 2: Compressed Wrapper  (e.g., layout.push_button("X", checked)) |
+-----------------------------------------------------------------------+
| Level 1: Low-Level Primitives (e.g., draw_big_text_button(...))       |
+-----------------------------------------------------------------------+

### State and Encapsulation
* **Keep Layout State Queryable:** Never make state or layout structures entirely private or opaque. If an agent or developer encounters an edge case that the high-level API doesn't support, they must be able to mix low-level primitive calls alongside high-level wrappers inside the same context without rewriting the entire module.

## Code Generation Checklists
### Before Creating an Abstraction / Helper
* Are you pulling out a pattern that has repeated at least 3+ times in the immediate context?
* Does the helper allow the caller to step down a level of abstraction if a slight variation occurs?
* If this helper introduces a strict parameter (like column_count), is it worth accepting that manual input to avoid complex framework overhead or frame-latency tracking? (Usually, yes).

### When Refactoring / Compressing Code
* Step 1: Isolate the repetitive chunks.
* Step 2: Package them into a local helper or layout state struct method.
* Step 3: Ensure the underlying components are public/accessible.
* Step 4: Verify that a developer can still weave raw primitive calls between your new compressed helpers.