import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  AlertTriangle,
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  ChevronLeft,
  ChevronRight,
  Crown,
  FileCode2,
  Key,
  Layers,
  Search,
  Table2,
} from 'lucide-react';
import { adminApi, viewerApi } from '../api/endpoints';
import { ApiError } from '../api/client';
import type {
  DatabaseAccessGroupsResponse,
  SchemaResponse,
  SchemaTable,
  TableDataResponse,
} from '../types/api';
import { Badge, EmptyState, ErrorState, LoadingState, PageHeader } from '../components/shared/UIStates';
import { ForbiddenPage } from './ForbiddenPage';

export function SchemaViewerPage() {
  const { id } = useParams<{ id: string }>();
  const dbId = Number(id);

  const [schemaData, setSchemaData] = useState<SchemaResponse | null>(null);
  const [approvers, setApprovers] = useState<DatabaseAccessGroupsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [forbidden, setForbidden] = useState(false);

  const [search, setSearch] = useState('');
  const [selectedTableKey, setSelectedTableKey] = useState<string>('');
  const [activeTab, setActiveTab] = useState<'structure' | 'data'>('structure');

  // Paginated table rows state
  const [rowData, setRowData] = useState<TableDataResponse | null>(null);
  const [rowLoading, setRowLoading] = useState(false);
  const [rowError, setRowError] = useState('');
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [orderBy, setOrderBy] = useState('');
  const [desc, setDesc] = useState(false);

  const loadSchema = useCallback(async () => {
    if (!Number.isInteger(dbId) || dbId <= 0) {
      setError('ID database tidak valid.');
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    setForbidden(false);
    try {
      const [schemaRes, approversRes] = await Promise.all([
        viewerApi.getSchema(dbId),
        adminApi.getDatabaseAccessGroups(dbId).catch(() => null),
      ]);
      setSchemaData(schemaRes);
      setApprovers(approversRes);
      const first = schemaRes.snapshot.tables[0];
      if (first) {
        setSelectedTableKey(`${first.schema}.${first.name}`);
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        setForbidden(true);
      } else {
        setError(err instanceof ApiError ? err.message : 'Gagal memuat skema database.');
      }
    } finally {
      setLoading(false);
    }
  }, [dbId]);

  useEffect(() => {
    void loadSchema();
  }, [loadSchema]);

  const tables = schemaData?.snapshot.tables ?? [];
  const filteredTables = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return tables;
    return tables.filter(
      (t) => t.name.toLowerCase().includes(q) || t.schema.toLowerCase().includes(q),
    );
  }, [tables, search]);

  const selectedTable: SchemaTable | undefined = useMemo(
    () => tables.find((t) => `${t.schema}.${t.name}` === selectedTableKey),
    [tables, selectedTableKey],
  );

  const loadRows = useCallback(async () => {
    if (!selectedTable || activeTab !== 'data') return;
    setRowLoading(true);
    setRowError('');
    try {
      const res = await viewerApi.getTableData(dbId, selectedTable.name, {
        schema: selectedTable.schema,
        page,
        pageSize,
        orderBy: orderBy || undefined,
        desc,
      });
      setRowData(res);
    } catch (err) {
      setRowError(err instanceof ApiError ? err.message : 'Gagal memuat baris data tabel.');
    } finally {
      setRowLoading(false);
    }
  }, [dbId, selectedTable, activeTab, page, pageSize, orderBy, desc]);

  useEffect(() => {
    void loadRows();
  }, [loadRows]);

  const handleSelectTable = (t: SchemaTable) => {
    setSelectedTableKey(`${t.schema}.${t.name}`);
    setPage(1);
    setOrderBy('');
    setDesc(false);
  };

  const handleSort = (col: string) => {
    if (orderBy === col) {
      setDesc((prev) => !prev);
    } else {
      setOrderBy(col);
      setDesc(false);
    }
    setPage(1);
  };

  if (forbidden) return <ForbiddenPage />;

  return (
    <div>
      <div className="mb-3">
        <Link
          to="/databases"
          className="inline-flex items-center gap-1.5 text-sm font-medium text-slate-600 hover:text-indigo-600"
        >
          <ArrowLeft className="h-4 w-4" />
          Kembali ke Daftar Database
        </Link>
      </div>

      <PageHeader
        title={`Database Explorer #${dbId}`}
        description={
          schemaData
            ? `Engine: ${schemaData.engine.toUpperCase()} · Hak akses efektif: ${schemaData.level_label} · ${tables.length} tabel`
            : 'Inspeksi skema, kolom, constraint, index, dan data tabel (read-only).'
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            {schemaData && (
              <Badge variant={schemaData.level === 2 ? 'emerald' : 'sky'}>
                {schemaData.level_label}
              </Badge>
            )}
            <Link to={`/databases/${dbId}/migrations`} className="btn-secondary text-xs">
              <FileCode2 className="h-3.5 w-3.5" />
              Migrations
            </Link>
          </div>
        }
      />

      {/* Approver groups / missing leader status */}
      {approvers && approvers.groups.length > 0 && (
        <div
          className={`mb-6 rounded-xl border p-4 text-sm ${
            approvers.has_missing_leaders
              ? 'border-amber-200 bg-amber-50 text-amber-900'
              : 'border-slate-200 bg-white text-slate-700'
          }`}
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2 font-semibold">
              {approvers.has_missing_leaders ? (
                <AlertTriangle className="h-4 w-4 text-amber-600" />
              ) : (
                <Crown className="h-4 w-4 text-indigo-600" />
              )}
              <span>Group Pemilik Akses & Approver ({approvers.groups.length} group)</span>
            </div>
            <Badge variant={approvers.has_missing_leaders ? 'amber' : 'emerald'}>
              {approvers.has_missing_leaders ? 'Pending — Ada Group Tanpa Leader' : 'Approver Siap'}
            </Badge>
          </div>
          {approvers.has_missing_leaders && (
            <p className="mt-1.5 text-xs text-amber-800">{approvers.pending_reason}</p>
          )}
          <div className="mt-2.5 flex flex-wrap gap-2">
            {approvers.groups.map((g) => (
              <span
                key={g.group_id}
                className="inline-flex items-center gap-1.5 rounded-lg border border-slate-200 bg-white px-2.5 py-1 text-xs"
              >
                <span className="font-semibold text-slate-900">{g.group_name}</span>
                <span className="text-slate-400">({g.level_label})</span>
                <span className="text-slate-300">·</span>
                {g.has_leader ? (
                  <span className="text-emerald-700">Leader: {g.leader_username}</span>
                ) : (
                  <span className="font-medium text-amber-700">Belum ada leader (Pending)</span>
                )}
              </span>
            ))}
          </div>
        </div>
      )}

      {loading && <LoadingState label="Membaca katalog skema database..." />}
      {!loading && error && <ErrorState message={error} onRetry={() => void loadSchema()} />}

      {!loading && !error && tables.length === 0 && (
        <EmptyState
          title="Tidak ada tabel pada database ini"
          description="Introspeksi berhasil namun belum ditemukan tabel non-sistem pada database target."
        />
      )}

      {!loading && !error && tables.length > 0 && (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-12">
          {/* Table List Sidebar */}
          <div className="card p-4 lg:col-span-4 xl:col-span-3">
            <div className="relative mb-3">
              <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-slate-400" />
              <input
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Cari tabel atau skema..."
                className="input-field pl-9"
              />
            </div>

            <div className="max-h-[60vh] space-y-1 overflow-y-auto pr-1">
              {filteredTables.map((t) => {
                const key = `${t.schema}.${t.name}`;
                const active = key === selectedTableKey;
                return (
                  <button
                    key={key}
                    type="button"
                    onClick={() => handleSelectTable(t)}
                    className={`flex w-full items-center justify-between rounded-lg px-3 py-2 text-left text-sm transition ${
                      active
                        ? 'bg-indigo-50 font-semibold text-indigo-700'
                        : 'text-slate-700 hover:bg-slate-100'
                    }`}
                  >
                    <span className="flex min-w-0 items-center gap-2">
                      <Table2 className="h-4 w-4 shrink-0 opacity-70" />
                      <span className="truncate">{t.name}</span>
                    </span>
                    <span className="ml-2 shrink-0 text-xs text-slate-400">{t.schema}</span>
                  </button>
                );
              })}
              {filteredTables.length === 0 && (
                <p className="py-6 text-center text-xs text-slate-500">Tabel tidak cocok.</p>
              )}
            </div>
          </div>

          {/* Table Detail + Data Panel */}
          <div className="min-w-0 lg:col-span-8 xl:col-span-9">
            {selectedTable && (
              <div className="card overflow-hidden">
                <div className="flex flex-col gap-3 border-b border-slate-200 bg-slate-50/70 px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0">
                    <p className="text-xs font-medium uppercase tracking-wider text-slate-500">
                      {selectedTable.schema}
                    </p>
                    <h2 className="truncate text-lg font-bold text-slate-900">
                      {selectedTable.name}
                    </h2>
                  </div>

                  <div className="inline-flex rounded-lg border border-slate-200 bg-white p-1">
                    <button
                      type="button"
                      onClick={() => setActiveTab('structure')}
                      className={`rounded-md px-3 py-1.5 text-xs font-medium transition ${
                        activeTab === 'structure'
                          ? 'bg-indigo-600 text-white'
                          : 'text-slate-600 hover:text-slate-900'
                      }`}
                    >
                      Struktur ({selectedTable.columns.length})
                    </button>
                    <button
                      type="button"
                      onClick={() => setActiveTab('data')}
                      className={`rounded-md px-3 py-1.5 text-xs font-medium transition ${
                        activeTab === 'data'
                          ? 'bg-indigo-600 text-white'
                          : 'text-slate-600 hover:text-slate-900'
                      }`}
                    >
                      Data Tabel (Read-Only)
                    </button>
                  </div>
                </div>

                {activeTab === 'structure' ? (
                  <div className="space-y-6 p-5">
                    {/* Columns */}
                    <div>
                      <h3 className="mb-2.5 flex items-center gap-2 text-sm font-semibold text-slate-900">
                        <Layers className="h-4 w-4 text-indigo-600" />
                        Columns ({selectedTable.columns.length})
                      </h3>
                      <div className="overflow-x-auto rounded-lg border border-slate-200">
                        <table className="min-w-full divide-y divide-slate-200 text-left text-sm">
                          <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-500">
                            <tr>
                              <th className="px-4 py-2.5">Kolom</th>
                              <th className="px-4 py-2.5">Tipe Data</th>
                              <th className="px-4 py-2.5">Nullable</th>
                              <th className="px-4 py-2.5">Default</th>
                            </tr>
                          </thead>
                          <tbody className="divide-y divide-slate-100 bg-white">
                            {selectedTable.columns.map((c) => (
                              <tr key={c.name}>
                                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs font-semibold text-slate-900">
                                  <span className="inline-flex items-center gap-1.5">
                                    {c.is_primary_key && (
                                      <Key className="h-3.5 w-3.5 text-amber-500" aria-label="Primary Key" />
                                    )}
                                    {c.name}
                                  </span>
                                </td>
                                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-indigo-700">
                                  {c.data_type}
                                </td>
                                <td className="whitespace-nowrap px-4 py-2.5 text-xs">
                                  {c.nullable ? (
                                    <Badge variant="neutral">NULL</Badge>
                                  ) : (
                                    <Badge variant="indigo">NOT NULL</Badge>
                                  )}
                                </td>
                                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-xs text-slate-600">
                                  {c.default ?? <span className="text-slate-400">—</span>}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </div>

                    {/* Constraints */}
                    <div>
                      <h3 className="mb-2.5 text-sm font-semibold text-slate-900">
                        Constraints ({selectedTable.constraints.length})
                      </h3>
                      {selectedTable.constraints.length === 0 ? (
                        <p className="text-xs text-slate-500">Tidak ada constraint terdaftar.</p>
                      ) : (
                        <div className="overflow-x-auto rounded-lg border border-slate-200">
                          <table className="min-w-full divide-y divide-slate-200 text-left text-xs">
                            <thead className="bg-slate-50 font-semibold uppercase text-slate-500">
                              <tr>
                                <th className="px-4 py-2.5">Nama</th>
                                <th className="px-4 py-2.5">Tipe</th>
                                <th className="px-4 py-2.5">Kolom</th>
                                <th className="px-4 py-2.5">Definisi / Referensi</th>
                              </tr>
                            </thead>
                            <tbody className="divide-y divide-slate-100 bg-white">
                              {selectedTable.constraints.map((c) => (
                                <tr key={c.name}>
                                  <td className="whitespace-nowrap px-4 py-2.5 font-mono font-medium text-slate-900">
                                    {c.name}
                                  </td>
                                  <td className="whitespace-nowrap px-4 py-2.5">
                                    <Badge
                                      variant={
                                        c.type === 'PRIMARY KEY'
                                          ? 'amber'
                                          : c.type === 'FOREIGN KEY'
                                            ? 'sky'
                                            : 'neutral'
                                      }
                                    >
                                      {c.type}
                                    </Badge>
                                  </td>
                                  <td className="whitespace-nowrap px-4 py-2.5 font-mono text-slate-700">
                                    {c.columns.join(', ') || '—'}
                                  </td>
                                  <td className="px-4 py-2.5 font-mono text-slate-600">
                                    {c.ref_table
                                      ? `→ ${c.ref_table}(${(c.ref_columns ?? []).join(', ')})`
                                      : c.definition || '—'}
                                  </td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      )}
                    </div>

                    {/* Indexes */}
                    <div>
                      <h3 className="mb-2.5 text-sm font-semibold text-slate-900">
                        Indexes ({selectedTable.indexes.length})
                      </h3>
                      {selectedTable.indexes.length === 0 ? (
                        <p className="text-xs text-slate-500">Tidak ada index terdaftar.</p>
                      ) : (
                        <div className="overflow-x-auto rounded-lg border border-slate-200">
                          <table className="min-w-full divide-y divide-slate-200 text-left text-xs">
                            <thead className="bg-slate-50 font-semibold uppercase text-slate-500">
                              <tr>
                                <th className="px-4 py-2.5">Nama</th>
                                <th className="px-4 py-2.5">Method</th>
                                <th className="px-4 py-2.5">Unique</th>
                                <th className="px-4 py-2.5">Kolom / Predicate</th>
                              </tr>
                            </thead>
                            <tbody className="divide-y divide-slate-100 bg-white">
                              {selectedTable.indexes.map((idx) => (
                                <tr key={idx.name}>
                                  <td className="whitespace-nowrap px-4 py-2.5 font-mono font-medium text-slate-900">
                                    {idx.name}
                                  </td>
                                  <td className="whitespace-nowrap px-4 py-2.5 uppercase text-slate-600">
                                    {idx.method}
                                  </td>
                                  <td className="whitespace-nowrap px-4 py-2.5">
                                    {idx.unique ? (
                                      <Badge variant="emerald">UNIQUE</Badge>
                                    ) : (
                                      <Badge variant="neutral">NON-UNIQUE</Badge>
                                    )}
                                  </td>
                                  <td className="px-4 py-2.5 font-mono text-slate-700">
                                    {idx.columns.join(', ')}
                                    {idx.predicate ? ` WHERE ${idx.predicate}` : ''}
                                  </td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      )}
                    </div>
                  </div>
                ) : (
                  <div className="p-5">
                    {rowLoading && <LoadingState label="Mengambil baris data (transaksi read-only)..." />}
                    {!rowLoading && rowError && (
                      <ErrorState message={rowError} onRetry={() => void loadRows()} />
                    )}
                    {!rowLoading && !rowError && rowData && (
                      <>
                        <div className="mb-3 flex flex-wrap items-center justify-between gap-2 text-xs text-slate-600">
                          <span>
                            Total <strong>{rowData.total.toLocaleString('id-ID')}</strong> baris ·
                            Halaman <strong>{rowData.page}</strong> dari{' '}
                            <strong>{rowData.total_pages}</strong>
                          </span>
                          <div className="flex items-center gap-2">
                            <label htmlFor="page-size" className="text-slate-500">
                              Baris/halaman:
                            </label>
                            <select
                              id="page-size"
                              value={pageSize}
                              onChange={(e) => {
                                setPageSize(Number(e.target.value));
                                setPage(1);
                              }}
                              className="rounded-md border border-slate-300 bg-white px-2 py-1 text-xs"
                            >
                              {[10, 20, 50, 100].map((n) => (
                                <option key={n} value={n}>
                                  {n}
                                </option>
                              ))}
                            </select>
                          </div>
                        </div>

                        {rowData.rows.length === 0 ? (
                          <EmptyState
                            title="Tabel kosong"
                            description="Belum ada baris data di tabel ini."
                          />
                        ) : (
                          <div className="overflow-x-auto rounded-lg border border-slate-200">
                            <table className="min-w-full divide-y divide-slate-200 text-left text-xs">
                              <thead className="bg-slate-50 font-semibold text-slate-600">
                                <tr>
                                  {rowData.columns.map((col) => (
                                    <th key={col} className="whitespace-nowrap px-3.5 py-2.5">
                                      <button
                                        type="button"
                                        onClick={() => handleSort(col)}
                                        className="inline-flex items-center gap-1 font-mono hover:text-indigo-600"
                                      >
                                        {col}
                                        {orderBy === col &&
                                          (desc ? (
                                            <ArrowDown className="h-3.5 w-3.5" />
                                          ) : (
                                            <ArrowUp className="h-3.5 w-3.5" />
                                          ))}
                                      </button>
                                    </th>
                                  ))}
                                </tr>
                              </thead>
                              <tbody className="divide-y divide-slate-100 bg-white font-mono">
                                {rowData.rows.map((r, rIdx) => (
                                  <tr key={rIdx} className="hover:bg-slate-50">
                                    {r.map((cell, cIdx) => (
                                      <td
                                        key={cIdx}
                                        className="max-w-xs truncate whitespace-nowrap px-3.5 py-2 text-slate-800"
                                        title={cell === null ? 'NULL' : String(cell)}
                                      >
                                        {cell === null ? (
                                          <span className="italic text-slate-400">NULL</span>
                                        ) : (
                                          String(cell)
                                        )}
                                      </td>
                                    ))}
                                  </tr>
                                ))}
                              </tbody>
                            </table>
                          </div>
                        )}

                        <div className="mt-4 flex items-center justify-between">
                          <button
                            type="button"
                            disabled={page <= 1}
                            onClick={() => setPage((p) => Math.max(1, p - 1))}
                            className="btn-secondary text-xs"
                          >
                            <ChevronLeft className="h-4 w-4" />
                            Sebelumnya
                          </button>
                          <span className="text-xs text-slate-500">
                            Hal. {rowData.page} / {rowData.total_pages}
                          </span>
                          <button
                            type="button"
                            disabled={page >= rowData.total_pages}
                            onClick={() => setPage((p) => p + 1)}
                            className="btn-secondary text-xs"
                          >
                            Berikutnya
                            <ChevronRight className="h-4 w-4" />
                          </button>
                        </div>
                      </>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
