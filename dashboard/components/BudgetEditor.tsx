'use client';
import { useState } from 'react';
import { btnGhost, inputCls } from '@/components/ui';

/** Inline monthly-budget editor: shows the budget, edits it, or clears it. */
export default function BudgetEditor({ id, current, onSave }: {
  id: string;
  current: number | null;
  onSave: (budget: number | null) => Promise<boolean>;
}) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState('');
  const [saving, setSaving] = useState(false);
  const [invalid, setInvalid] = useState(false);

  async function save(b: number | null) {
    setSaving(true);
    const ok = await onSave(b);
    setSaving(false);
    if (ok) setEditing(false);
  }

  if (!editing) {
    return (
      <button
        className="text-xs text-neutral-400 underline decoration-neutral-700 underline-offset-2 hover:text-neutral-200"
        onClick={() => { setValue(current === null ? '' : String(current)); setInvalid(false); setEditing(true); }}
      >
        {current === null ? 'Set monthly budget' : 'Edit budget'}
      </button>
    );
  }

  return (
    <form
      className="flex items-center gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        const n = Number(value);
        if (value.trim() === '' || !Number.isFinite(n) || n < 0) { setInvalid(true); return; }
        save(n);
      }}
    >
      <label htmlFor={`budget-${id}`} className="text-xs text-neutral-400">Budget $</label>
      <input
        id={`budget-${id}`} type="number" min="0" step="0.0001" inputMode="decimal" autoFocus
        className={`${inputCls} h-8 w-28 py-1 text-xs`} value={value}
        aria-invalid={invalid} onChange={(e) => { setValue(e.target.value); setInvalid(false); }}
      />
      <span className="text-xs text-neutral-500">/ month</span>
      <button type="submit" className={btnGhost} disabled={saving}>{saving ? 'Saving…' : 'Save'}</button>
      {current !== null && <button type="button" className={btnGhost} disabled={saving} onClick={() => save(null)}>Remove</button>}
      <button type="button" className={btnGhost} disabled={saving} onClick={() => setEditing(false)}>Cancel</button>
      {invalid && <span className="text-xs text-red-400" role="alert">Enter an amount of 0 or more.</span>}
    </form>
  );
}
