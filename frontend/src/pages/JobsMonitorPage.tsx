import { Activity, Info } from 'lucide-react';
import { Badge, EmptyState, PageHeader } from '../components/shared/UIStates';

const plannedStatuses = [
  { label: 'Pending (Queue)', variant: 'neutral' as const },
  { label: 'Running', variant: 'sky' as const },
  { label: 'Partial', variant: 'amber' as const },
  { label: 'Partially Reverted', variant: 'amber' as const },
  { label: 'Success', variant: 'emerald' as const },
  { label: 'Failed', variant: 'rose' as const },
];

export function JobsMonitorPage() {
  return (
    <div>
      <PageHeader
        title="Job Monitoring & Batch Progress"
        description="Pemantauan real-time eksekusi asynchronous worker, progress batch/cursor, retry, resume, dan kompensasi (domain Orang 4)."
      />

      <div className="mb-6 rounded-xl border border-indigo-200 bg-indigo-50/70 p-4 text-xs text-indigo-900">
        <div className="flex items-start gap-2.5">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-indigo-600" />
          <div className="space-y-2">
            <p>
              <strong>Slot Integrasi Orang 4:</strong> Halaman ini siap dihubungkan dengan API job
              status/progress (<code className="rounded bg-white/80 px-1 py-0.5 font-mono">GET /api/jobs</code>,{' '}
              <code className="rounded bg-white/80 px-1 py-0.5 font-mono">POST /api/jobs/:id/resume</code>,{' '}
              <code className="rounded bg-white/80 px-1 py-0.5 font-mono">POST /api/jobs/:id/cancel</code>)
              dengan polling aman yang tidak membuat job baru.
            </p>
            <div className="flex flex-wrap gap-1.5">
              {plannedStatuses.map((s) => (
                <Badge key={s.label} variant={s.variant}>
                  {s.label}
                </Badge>
              ))}
            </div>
          </div>
        </div>
      </div>

      <EmptyState
        title="Belum ada job eksekusi aktif"
        description="Setelah modul queue-worker Orang 4 aktif, daftar job asynchronous, jumlah batch/row committed, checkpoint, dan kontrol retry/resume akan muncul di sini."
        action={
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500">
            <Activity className="h-4 w-4" />
            Siap integrasi API Orang 4
          </span>
        }
      />
    </div>
  );
}
