import clsx from 'clsx';
import { providerLabelFor } from '@/lib/api';

/** Provider name chip; the provider's own model id is shown beside it and in the tooltip. */
export default function ProviderChip({
  provider, modelId, available = true,
}: { provider: string; modelId?: string; available?: boolean }) {
  const title = `${providerLabelFor(provider)}${modelId ? ` · ${modelId}` : ''}${available ? '' : ' (unavailable)'}`;
  return (
    <span
      title={title}
      className={clsx(
        'inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs',
        available ? 'border-neutral-700 bg-neutral-800 text-neutral-300' : 'border-neutral-800 bg-neutral-900 text-neutral-600 line-through',
      )}
    >
      <span className="font-medium text-indigo-300">{provider}</span>
      {modelId && <span className="truncate font-mono text-neutral-400">{modelId}</span>}
      {!available && <span className="sr-only">(unavailable)</span>}
    </span>
  );
}
