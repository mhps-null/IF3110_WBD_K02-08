export interface Principal {
  id: number;
  username: string;
  is_platform_admin: boolean;
}

export interface UserGroupMembership {
  group_id: number;
  group_name: string;
  description: string;
  is_leader: boolean;
}

export interface AccessibleDatabase {
  db_id: number;
  name: string;
  engine?: string;
  level: 0 | 1 | 2;
  level_label: string;
}

export interface AuthMeResponse {
  user: Principal;
  groups: UserGroupMembership[];
  accessible_databases: AccessibleDatabase[];
  csrf_token?: string;
}

export interface AdminUser {
  id: number;
  username: string;
  is_admin: boolean;
  is_active: boolean;
  created_at: string;
}

export interface AdminGroup {
  id: number;
  name: string;
  description: string;
  leader_user_id: number | null;
  leader_username: string | null;
  leader_is_active: boolean;
  has_leader: boolean;
  member_count: number;
}

export interface GroupDetailResponse {
  group: AdminGroup;
  members: AdminUser[];
  available_users: AdminUser[];
}

export interface AdminDatabaseSummary {
  id: number;
  name: string;
}

export interface AccessMatrixEntry {
  db_id: number;
  group_id: number;
  database_name: string;
  group_name: string;
  level: number;
  level_label: string;
  leader_user_id: number | null;
  leader_username: string | null;
  has_leader: boolean;
}

export interface AccessMatrixResponse {
  databases: AdminDatabaseSummary[];
  groups: AdminGroup[];
  access_entries: AccessMatrixEntry[];
}

export interface DatabaseGroupAccess {
  group_id: number;
  group_name: string;
  db_id: number;
  level: number;
  level_label: string;
  leader_user_id: number | null;
  leader_username: string | null;
  leader_is_active: boolean;
  has_leader: boolean;
}

export interface DatabaseAccessGroupsResponse {
  db_id: number;
  groups: DatabaseGroupAccess[];
  missing_leader_groups: Array<{ group_id: number; group_name: string }>;
  has_missing_leaders: boolean;
  approval_status: 'ready' | 'pending_leader_assignment';
  pending_reason: string;
}

export interface DBConnection {
  id: number;
  name: string;
  engine: 'postgres' | 'mysql' | 'mariadb';
  host: string;
  port: number;
  database_name: string;
  username: string;
  ssl_mode: string;
  created_at: string;
  updated_at: string;
}

export interface PoolStat {
  db_id: number;
  db_name: string;
  engine: string;
  open_conns: number;
  in_use: number;
  idle: number;
  wait_count: number;
  wait_duration: number;
}

export interface TableColumn {
  name: string;
  data_type: string;
  nullable: boolean;
  default: string | null;
  is_primary_key: boolean;
  ordinal: number;
}

export interface TableConstraint {
  name: string;
  type: string;
  columns: string[];
  ref_table?: string;
  ref_columns?: string[];
  on_delete?: string;
  on_update?: string;
  definition?: string;
}

export interface TableIndex {
  name: string;
  columns: string[];
  unique: boolean;
  method: string;
  predicate?: string;
  key_definitions?: string[];
}

export interface SchemaTable {
  schema: string;
  name: string;
  columns: TableColumn[];
  constraints: TableConstraint[];
  indexes: TableIndex[];
}

export interface SchemaResponse {
  db_id: number;
  engine: string;
  level: number;
  level_label: string;
  snapshot: {
    engine: string;
    schema: string;
    tables: SchemaTable[];
  };
}

export interface TableDataResponse {
  db_id: number;
  engine: string;
  schema: string;
  table: string;
  columns: string[];
  rows: Array<Array<unknown>>;
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
  order_by: string;
  desc: boolean;
}

export interface AuditEntry {
  id: number;
  created_at: string;
  actor: string;
  event: string;
  database: string;
  status: string;
  correlation_id?: string;
  trace_id?: string;
  span_id?: string;
  details?: Record<string, unknown>;
}

export interface AuditPage {
  items: AuditEntry[];
  total: number;
  page: number;
  page_size: number;
}

export interface MigrationRecord {
  id: number;
  db_id: number;
  version: number | null;
  title: string;
  sql_text: string;
  status: string;
  created_by: number;
  created_by_name?: string;
  created_at: string;
  executed_by?: number | null;
  executed_by_name?: string | null;
  executed_at?: string | null;
  error_message?: string | null;
  checksum?: string | null;
}
