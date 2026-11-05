# Custom tool search

`Config.SearchFunc` lets an application plug its existing retrieval index into
Tool Search while the middleware continues to resolve and load registered tools.
See `ExampleConfig_SearchFunc` in `example_test.go` for a runnable example with
no model credentials or external service.

The callback receives the request context, a trimmed query, and a positive result
limit. An omitted or nonpositive `max_results` becomes 5. Return tool names in
ranked order; the middleware drops unknown names and duplicates, preserves the
first occurrence of each registered name, and applies the limit after filtering.
Names are matched exactly. Returning no names means no matches. Errors propagate
without falling back to keyword search, and cancellation is checked before and
after the callback. A callback must itself stop promptly when its context ends;
the middleware does not run it in a detached goroutine.

The callback and its captured resources are shared across agent runs. The
application owns index creation, updates, synchronization, and cleanup. Only
names in `Config.DynamicTools` can be resolved. This is a ranking extension, not
an authorization boundary: `select:name1,name2` always bypasses the callback and
preserves the existing exact selection behavior, including order, duplicates,
and ignoring `max_results`.

With a nil callback, built-in keyword matching, `+` required terms, tie-breaking,
result limits, and tool descriptions are unchanged. With a callback, non-`select:`
query syntax (including `+`) belongs to that implementation, and the search tool
uses a generic search description rather than promising keyword semantics.

Both `New` and `NewTyped` support the callback. It runs for ordinary client-side
search and for client-executed search calls when `UseModelToolSearch` is true.
Retrieval performed entirely on the provider's server never invokes it. This hook
does not add a model adapter, an embedding backend, or a relevance improvement by
itself; those require separate integrations and evaluation.
