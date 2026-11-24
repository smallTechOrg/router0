'use client';
import { useState } from 'react';
import { btnGhost, inputCls } from '@/components/ui';

/**
 * Shows a token's model allowlist and edits it inline. The list is typed as
 * comma-separated model or alias names; "Allow all" clears it.
 */
export default function AllowedModelsEditor({ id, current, canWrite, onSave }: {
  id: string;
  current: string[] | null;
  canWrite: boolean;
  onSave: (models: string[] | null) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState('');
  const [saving, setSaving] = useState(false);
  const [invalid, setInvalid] = useState(false);

  async function save(models: string[] | null) {
    setSaving(true);
    const ok = await onSave(models);
    setSaving(false);
    if (ok) setEditing(false);
  }

  if (!editing) {
    return (
      <div className="flex flex-wrap items-center gap-1.5">
        {current === null ? (
          <span className="text-xs text-neutral-400">All models</span>
        ) : (
          current.map((m) => (
            <span key={m} className="rounded bg-neutral-800 px-1.5 py-0.5 font-mono text-[11px] text-neutral-200">{m}</span>
          ))
        )}
        {canWrite && (
          <button
            className="text-xs text-neutral-400 underline decoration-neutral-700 underline-offset-2 hover:text-neutral-200"
            onClick={() => { setValue((current ?? []).join(', ')); setInvalid(false); setEditing(true); }}
          >
            {current === null ? 'Restrict' : 'Edit'}
          </button>
        )}
      </div>
    );
  }

  return (
    <form
      className="space-y-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        const models = value.split(',').map((s) => s.trim()).filter(Boolean);
        if (models.length === 0) { setInvalid(true); return; }
        save(models);
      }}
    >
      <label htmlFor={`models-${id}`} className="sr-only">Allowed models, comma-separated</label>
      <input
        id={`models-${id}`} autoFocus placeholder="llama-3.3-70b, fast"
        className={`${inputCls} h-8 w-64 py-1 font-mono text-xs`} value={value}
        aria-invalid={invalid} aria-describedby={`models-help-${id}`}
        onChange={(e) => { setValue(e.target.value); setInvalid(false); }}
      />
      <p id={`models-help-${id}`} className="text-[11px] text-neutral-500">Model or alias names, comma-separated.</p>
      <div className="flex items-center gap-1.5">
        <button type="submit" className={btnGhost} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
        {current !== null && <button type="button" className={btnGhost} disabled={saving} onClick={() => save(null)}>Allow all</button>}
        <button type="button" className={btnGhost} disabled={saving} onClick={() => setEditing(false)}>Cancel</button>
      </div>
      {invalid && <p className="text-xs text-red-400" role="alert">List at least one model, or use Allow all.</p>}
    </form>
  );
}
