<script>
  // Renders the active themed confirm/prompt dialog (see lib/dialog.js). Mount
  // once in the app shell. Resolves the pending promise on confirm/cancel.
  import { tick } from "svelte";
  import { dialogState } from "../lib/dialog.js";

  let inputEl = $state(null);
  let value = $state("");

  // Focus the input (prompt) or the confirm button when a dialog opens.
  $effect(() => {
    if ($dialogState) {
      value = $dialogState.value ?? "";
      tick().then(() => inputEl?.focus());
    }
  });

  function settle(result) {
    const d = $dialogState;
    dialogState.set(null);
    d?.resolve(result);
  }
  function onConfirm() {
    settle($dialogState.kind === "prompt" ? value : true);
  }
  function onCancel() {
    settle($dialogState.kind === "prompt" ? null : false);
  }
  function onKey(e) {
    if (!$dialogState) return;
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    } else if (e.key === "Enter" && $dialogState.kind !== "prompt") {
      e.preventDefault();
      onConfirm();
    }
  }
</script>

<svelte:window onkeydown={onKey} />

{#if $dialogState}
  <div class="fixed inset-0 z-[110] bg-black/60 grid place-items-center p-4" role="presentation" onclick={onCancel}>
    <div
      class="card w-full max-w-md p-5 space-y-4"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      aria-label={$dialogState.title || "Confirm"}
      onclick={(e) => e.stopPropagation()}
    >
      {#if $dialogState.title}
        <h2 class="text-lg font-semibold">{$dialogState.title}</h2>
      {/if}
      {#if $dialogState.body}
        <p class="text-sm text-muted whitespace-pre-line">{$dialogState.body}</p>
      {/if}
      {#if $dialogState.kind === "prompt"}
        {#if $dialogState.label}
          <label class="label" for="dlg-input">{$dialogState.label}</label>
        {/if}
        <input
          id="dlg-input"
          bind:this={inputEl}
          bind:value
          class="input"
          placeholder={$dialogState.placeholder || ""}
          onkeydown={(e) => e.key === "Enter" && onConfirm()}
        />
      {/if}
      <div class="flex gap-2 pt-1">
        <button class="btn-ghost flex-1" onclick={onCancel}>{$dialogState.cancelText}</button>
        <button
          class="{$dialogState.danger ? 'btn-danger' : 'btn-primary'} flex-1"
          onclick={onConfirm}>{$dialogState.confirmText}</button>
      </div>
    </div>
  </div>
{/if}
