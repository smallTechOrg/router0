import clsx from 'clsx';

type Status = 'success' | 'error' | 'pending';

interface StatusBadgeProps {
  status: Status;
}

const statusStyles: Record<Status, string> = {
  success: 'bg-emerald-500/15 text-emerald-400 ring-emerald-500/20',
  error: 'bg-red-500/15 text-red-400 ring-red-500/20',
  pending: 'bg-amber-500/15 text-amber-400 ring-amber-500/20',
};

const statusLabels: Record<Status, string> = {
  success: 'Success',
  error: 'Error',
  pending: 'Pending',
};

export default function StatusBadge({ status }: StatusBadgeProps) {
  return (
    <span
      className={clsx(
        'inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ring-1 ring-inset',
        statusStyles[status],
      )}
    >
      {statusLabels[status]}
    </span>
  );
}
