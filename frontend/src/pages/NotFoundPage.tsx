import { Link } from 'react-router-dom';
import { FileQuestion } from 'lucide-react';

export function NotFoundPage() {
  return (
    <div className="flex min-h-[70vh] flex-col items-center justify-center px-4 text-center">
      <div className="rounded-full bg-slate-100 p-4 text-slate-500">
        <FileQuestion className="h-10 w-10" />
      </div>
      <p className="mt-4 text-sm font-semibold uppercase tracking-wider text-slate-500">404 Not Found</p>
      <h1 className="mt-1 text-2xl font-bold text-slate-900">Halaman Tidak Ditemukan</h1>
      <p className="mt-2 max-w-md text-sm text-slate-600">
        Rute yang kamu tuju tidak tersedia di aplikasi ini.
      </p>
      <Link to="/" className="btn-primary mt-6">
        Kembali ke Dashboard
      </Link>
    </div>
  );
}
