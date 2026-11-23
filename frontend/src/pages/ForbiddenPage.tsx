import { Link } from 'react-router-dom';
import { ShieldAlert } from 'lucide-react';

export function ForbiddenPage() {
  return (
    <div className="flex min-h-[70vh] flex-col items-center justify-center px-4 text-center">
      <div className="rounded-full bg-rose-100 p-4 text-rose-600">
        <ShieldAlert className="h-10 w-10" />
      </div>
      <p className="mt-4 text-sm font-semibold uppercase tracking-wider text-rose-600">403 Forbidden</p>
      <h1 className="mt-1 text-2xl font-bold text-slate-900">Akses Ditolak</h1>
      <p className="mt-2 max-w-md text-sm text-slate-600">
        Akun kamu tidak memiliki hak akses untuk membuka halaman atau operasi ini. Otorisasi
        diperiksa langsung pada backend.
      </p>
      <Link to="/" className="btn-primary mt-6">
        Kembali ke Dashboard
      </Link>
    </div>
  );
}
