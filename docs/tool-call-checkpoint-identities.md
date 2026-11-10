# Tool-call identities and persisted checkpoints

ToolsNode preserves unique, non-empty model tool-call IDs as execution keys. Empty or repeated IDs receive deterministic, batch-local keys based on their original position. The generated namespace is reserved against all model IDs in the batch. Both the invocation context and legacy interrupt wrapper use the tool name and this key in their address.

Model-facing ToolCallID values, ToolInput.CallID and GetToolCallID remain unchanged. ToolsInterruptAndRerunExtra.ToolCallKeys aligns with ToolCalls and provides the keys used by ExecutedTools, ExecutedEnhancedTools, RerunTools and RerunExtraMap. These keys are opaque; consumers should use that mapping rather than infer an index from their spelling.

New checkpoints record the key format version. Old checkpoints with unique non-empty model IDs retain their result-map keys and modern interrupt addresses. Old checkpoints containing empty or repeated IDs are rejected before tool dispatch: the original maps may already have overwritten a completed result, so automatically replaying such a checkpoint could repeat external side effects. Finish those runs with the previous version or reconcile their effects explicitly before starting a new run. Rolling a new-format ambiguous-ID checkpoint back to an older Eino version is not supported.

The legacy wrapper address shape changes from tool:<callID> to tool:<name>:<key>. Old legacy interrupts are not transparently migrated. The separate legacy interrupt-ID/resume-state problem tracked by #1269 is outside this patch; matching addresses alone does not establish that legacy UUID-based BatchResumeWithData works. The regression tests verify legacy address consistency and modern selective resume separately.
