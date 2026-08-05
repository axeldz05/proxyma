# SKILL: Test-Driven Development

## Core Directives

* Run the absolute lifecycle of every implementation using a strict **Red-Green-Refactor loop**. Do not write business logic prior to writing a failing test.
* For every individual iteration (Failing Test $\rightarrow$ Green Solution $\rightarrow$ Refactor), automatically commit changes to the branch `agent-{task-name}`.
* Every commit message must be a single, short sentence written in imperative English (e.g., `test: add mTLS unauthorized peer rejection`).

## 1. The Refactoring Engine: Semantic Compression

During the **Refactor** phase of the TDD loop, treat the codebase like a dictionary compressor running continuously. Do not guess future architecture; compress *only* when concrete duplication emerges naturally.

### Rules of Compression

1. **Write Specific Code First:** In the *Green* phase, implement exactly what the test case needs. Ignore reusability entirely to move fast.
2. **The Two-Occurrence Rule:** Only extract common logic, helper utilities, or state structures when you see the exact same pattern occur at least twice. Never abstract from a single use-case.
3. **Bottom-Up Compression Tactics:**
* Group repeatedly used parameters or stack states into a structural layout or state machine (e.g., encapsulating boilerplate into test servers or execution contexts).
* Wrap repeated testing or network code blocks into plain functions first.
* Turn those functions into methods only if they naturally operate on the same data structures.
4. **Keep the Call Site Readable:** The final code and test cases must read like a step-by-step recipe, minimizing syntactical noise.

## 2. Structural Guardrail: Continuous Granularity

Compression must never come at the cost of visibility. When bundling low-level operations into higher-level abstractions during refactoring, **maintain continuous granularity**.

* **No Functional Holes:** High-level wrappers must always be trivially replaceable by the lower-level primitives they compose.
* **Keep State Queryable:** Never make state, internal configuration, or testing assertions entirely private or opaque. If a test case hits an edge case that a high-level helper doesn't support, you must be able to mix raw low-level primitive calls side-by-side with your compressed helpers without rewriting the entire test suite.

## 3. Go-Specific P2P Testing Patterns

When implementing code or tests within `Proxyma`, match the existing idiomatic standards found in `p2p_test.go` and `server_test.go`:

### Concurrency and Isolation

* **Parallel Execution:** Invoke `t.Parallel()` at the top of every top-level test function.
* **Hermetic Environments:** Use `t.TempDir()` for all disk state simulations (e.g., cluster certs, local node storage, or VFS paths). Never hardcode paths.
* **Deterministic Resource Cleanup:** Utilize `t.Cleanup(func)` or `defer` immediately following the creation of mock servers, network components, or file descriptors to prevent leaks.

### Network and HTTP Mocking

* **No Real Sockets Unless Necessary:** Prioritize using `httptest.NewRecorder()` alongside `httptest.NewRequest()` for synchronous endpoint evaluation.
* **mTLS Cluster Simulation:** When testing P2P transport security, instantiate servers via `httptest.NewUnstartedServer()` or `httptest.NewTLSServer()` using the cluster's loaded mTLS configurations (`p2p.LoadNodeTLS`).

### Asynchronous & Timing Assertions

* **Avoid Flaky Fixed Sleeps:** For background synchronization or network propagation, **never** use static `time.Sleep()` statements for verification.
* **Controlled Polling:** Use `require.Eventually` with aggressive polling intervals (e.g., `100*time.Millisecond`) and strict timeouts (e.g., `2*time.Second` to `3*time.Second`) to verify cluster convergence or asynchronous state updates.
* **Context Lifecycles:** Ensure network execution blocks accurately respect incoming `context.Context` closures and timeouts.

## 4. What to Avoid

* **No Upfront Design:** No interface definitions or complex structures before writing the concrete implementation that proves their necessity.
* **No Structural Rigidity:** Avoid deep abstraction layers or mocking frameworks that restrict stepping down to the raw HTTP/network layer. Keep dependencies lean.
* **No Premature Compression:** Do not compress code if the two patterns look similar but diverge fundamentally in behavior or semantics. Avoid forcing shared code paths onto distinct domain operations.