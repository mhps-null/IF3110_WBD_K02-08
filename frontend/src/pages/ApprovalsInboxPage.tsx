import { CheckSquare, Crown, Info } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { Badge, EmptyState, PageHeader } from '../components/shared/UIStates';

export function ApprovalsInboxPage() {
  const { groups } = useAuth();
  const leaderGroups = groups.filter((g) => g.is_leader);

  return (
    <div>
      <PageHeader
        title="Review Inbox & Approvals"
        description="Daftar pengajuan migration (Up, Rollback, Forward) yang menunggu persetujuan Group Leader."
      />

      <div className="card mb-6 p-5">
        <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold text-slate-900">
          <Crown className="h-4 w-4 text-amber-500" />
          Group yang Kamu Pimpin ({leaderGroups.length})
        </h2>
        {leaderGroups.length === 0 ? (
          <p className="text-sm text-slate-600">
            Saat ini kamu belum ditunjuk sebagai Group Leader pada group mana pun. Hanya Group
            Leader dari group yang memiliki akses ke database target yang dapat memberikan approval.
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {leaderGroups.map((g) => (
              <Badge key={g.group_id} variant="amber">
                <Crown className="h-3 w-3" />
                {g.group_name}
              </Badge>
            ))}
          </div>
        )}
      </div>

      <div className="mb-6 rounded-xl border border-indigo-200 bg-indigo-50/70 p-4 text-xs text-indigo-900">
        <div className="flex items-start gap-2.5">
          <Info className="mt-0.5 h-4 w-4 shrink-0 text-indigo-600" />
          <p>
            <strong>Slot Integrasi Orang 3:</strong> Komponen ini siap dihubungkan dengan endpoint
            Approval Workflow milik Orang 3 (approve/reject multi-group, satu aksi leader mewakili
            seluruh group yang dipimpinnya, status Pending untuk group tanpa leader).
          </p>
        </div>
      </div>

      <EmptyState
        title="Belum ada pengajuan menunggu review"
        description="Pengajuan migration yang membutuhkan persetujuan leader akan tampil di sini setelah modul approval Orang 3 terhubung."
        action={
          <span className="inline-flex items-center gap-1.5 text-xs font-medium text-slate-500">
            <CheckSquare className="h-4 w-4" />
            Siap integrasi API Orang 3
          </span>
        }
      />
    </div>
  );
}
