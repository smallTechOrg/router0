'use client';
import { useEffect, useRef } from 'react';
import { btnPrimary, CopyButton } from './ui';

/** Shows the full service token exactly once. */
export default function TokenCreatedModal({ name, token, onClose }: { name: string; token: string; onClose: () => void }) {
  const closeRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    closeRef.current?.focus();
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4" role="presentation">
      <div role="dialog" aria-modal="true" aria-labelledby="token-modal-title" className="w-full max-w-lg rounded-xl border border-neutral-700 bg-neutral-900 p-6 shadow-2xl">
        <h2 id="token-modal-title" className="text-lg font-bold text-white">Token created: {name}</h2>
        <p className="mt-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-300">
          This token will not be shown again. Copy it now and store it somewhere safe.
        </p>
        <code className="mt-4 block break-all rounded-lg border border-indigo-500/30 bg-neutral-950 px-4 py-3 font-mono text-sm text-indigo-300">
          {token}
        </code>
        <div className="mt-5 flex items-center justify-end gap-2">
          <CopyButton text={token} label="Copy token" />
          <button ref={closeRef} onClick={onClose} className={btnPrimary}>I have saved it</button>
        </div>
      </div>
    </div>
  );
}
