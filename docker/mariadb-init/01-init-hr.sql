USE test_hr_mariadb;

-- 1. Departments
CREATE TABLE departments (
    id INT AUTO_INCREMENT PRIMARY KEY,
    code VARCHAR(20) NOT NULL UNIQUE,
    name VARCHAR(100) NOT NULL,
    budget DECIMAL(15,2) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_dept_budget CHECK (budget > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2. Positions
CREATE TABLE positions (
    id INT AUTO_INCREMENT PRIMARY KEY,
    title VARCHAR(100) NOT NULL UNIQUE,
    min_salary DECIMAL(12,2) NOT NULL,
    max_salary DECIMAL(12,2) NOT NULL,
    CONSTRAINT chk_pos_min_salary CHECK (min_salary > 0),
    CONSTRAINT chk_pos_salary_range CHECK (max_salary >= min_salary)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 3. Employees
CREATE TABLE employees (
    id INT AUTO_INCREMENT PRIMARY KEY,
    department_id INT NOT NULL,
    position_id INT NOT NULL,
    nip VARCHAR(30) NOT NULL UNIQUE,
    full_name VARCHAR(150) NOT NULL,
    email VARCHAR(150) NOT NULL UNIQUE,
    employment_type ENUM('permanent', 'contract', 'intern') DEFAULT 'permanent',
    salary DECIMAL(12,2) NOT NULL,
    hire_date DATE NOT NULL,
    is_active TINYINT(1) DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_emp_salary CHECK (salary > 0),
    CONSTRAINT fk_emp_department FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
    CONSTRAINT fk_emp_position FOREIGN KEY (position_id) REFERENCES positions(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 4. Performance Reviews
CREATE TABLE performance_reviews (
    id INT AUTO_INCREMENT PRIMARY KEY,
    employee_id INT NOT NULL,
    review_period VARCHAR(20) NOT NULL,
    score INT NOT NULL,
    notes TEXT,
    reviewed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_review_score CHECK (score >= 1 AND score <= 100),
    CONSTRAINT fk_review_employee FOREIGN KEY (employee_id) REFERENCES employees(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Indexes
CREATE INDEX idx_emp_department ON employees(department_id);
CREATE INDEX idx_emp_type ON employees(employment_type);
CREATE INDEX idx_emp_salary ON employees(salary);
CREATE INDEX idx_perf_emp ON performance_reviews(employee_id);

-- Seed Data: Departments
INSERT INTO departments (code, name, budget) VALUES
('DEPT-ENG', 'Engineering & Technology', 12500000000.00),
('DEPT-HC', 'Human Capital & Culture', 2500000000.00),
('DEPT-FIN', 'Finance & Accounting', 3800000000.00),
('DEPT-PRD', 'Product & Design', 4500000000.00);

-- Seed Data: Positions
INSERT INTO positions (title, min_salary, max_salary) VALUES
('Junior Software Engineer', 8000000.00, 14000000.00),
('Software Engineer', 14000000.00, 24000000.00),
('Senior Software Engineer', 24000000.00, 40000000.00),
('Engineering Manager', 38000000.00, 65000000.00),
('Product Manager', 18000000.00, 35000000.00),
('HR Operations Specialist', 7500000.00, 15000000.00),
('Financial Analyst', 9000000.00, 18000000.00);

-- Seed Data: Employees (20 rows)
INSERT INTO employees (department_id, position_id, nip, full_name, email, employment_type, salary, hire_date) VALUES
(1, 4, 'NIP-ENG-001', 'Bambang Sudarsono', 'bambang.eng@company.id', 'permanent', 48000000.00, '2020-03-01'),
(1, 3, 'NIP-ENG-002', 'Aditia Pratama', 'aditia.p@company.id', 'permanent', 32000000.00, '2021-06-15'),
(1, 3, 'NIP-ENG-003', 'Rina Wulandari', 'rina.w@company.id', 'permanent', 30000000.00, '2021-08-01'),
(1, 2, 'NIP-ENG-004', 'Deni Setiawan', 'deni.s@company.id', 'permanent', 19500000.00, '2022-01-10'),
(1, 2, 'NIP-ENG-005', 'Maya Anggraini', 'maya.a@company.id', 'permanent', 18000000.00, '2022-04-18'),
(1, 1, 'NIP-ENG-006', 'Fajar Ramadhan', 'fajar.r@company.id', 'contract', 11000000.00, '2023-07-01'),
(1, 1, 'NIP-ENG-007', 'Gita Cahyani', 'gita.c@company.id', 'contract', 10500000.00, '2023-09-15'),
(1, 1, 'NIP-ENG-008', 'Hendra Kusuma', 'hendra.k@company.id', 'intern', 8000000.00, '2024-02-01'),
(4, 5, 'NIP-PRD-001', 'Clara Shinta', 'clara.s@company.id', 'permanent', 28000000.00, '2021-11-01'),
(4, 5, 'NIP-PRD-002', 'Dimas Prasetya', 'dimas.p@company.id', 'permanent', 23000000.00, '2022-05-15'),
(2, 6, 'NIP-HC-001', 'Dewi Susanti', 'dewi.s@company.id', 'permanent', 14500000.00, '2020-09-01'),
(2, 6, 'NIP-HC-002', 'Irfan Hakim', 'irfan.h@company.id', 'contract', 9500000.00, '2023-03-01'),
(3, 7, 'NIP-FIN-001', 'Farida Nur', 'farida.n@company.id', 'permanent', 17000000.00, '2021-02-15'),
(3, 7, 'NIP-FIN-002', 'Kevin Sanjaya', 'kevin.s@company.id', 'permanent', 14000000.00, '2022-08-01'),
(1, 2, 'NIP-ENG-009', 'Lestari Indah', 'lestari.i@company.id', 'permanent', 17500000.00, '2022-10-01'),
(1, 2, 'NIP-ENG-010', 'Muhammad Farhan', 'm.farhan@company.id', 'permanent', 16500000.00, '2023-01-15'),
(4, 5, 'NIP-PRD-003', 'Nadya Putri', 'nadya.p@company.id', 'contract', 19000000.00, '2023-06-01'),
(1, 1, 'NIP-ENG-011', 'Oscar Darmawan', 'oscar.d@company.id', 'intern', 8500000.00, '2024-01-10'),
(2, 6, 'NIP-HC-003', 'Putri Ayu', 'putri.a@company.id', 'contract', 8500000.00, '2023-11-01'),
(3, 7, 'NIP-FIN-003', 'Rendy Pandugo', 'rendy.p@company.id', 'contract', 10500000.00, '2023-12-01');

-- Seed Data: Performance Reviews (10 rows)
INSERT INTO performance_reviews (employee_id, review_period, score, notes) VALUES
(1, '2023-Q4', 94, 'Kepemimpinan teknis luar biasa dalam peluncuran arsitektur mikro.'),
(2, '2023-Q4', 90, 'Pengembangan backend sangat solid dan tepat waktu.'),
(3, '2023-Q4', 92, 'Mentoring junior engineer sangat baik dan proaktif.'),
(4, '2023-Q4', 85, 'Penyelesaian fitur checkout stabil tanpa bug kritis.'),
(5, '2023-Q4', 88, 'Kinerja konsisten dengan penulisan unit test menyeluruh.'),
(9, '2023-Q4', 91, 'Roadmap produk tersusun dengan jelas dan terukur.'),
(10, '2023-Q4', 87, 'User story dan kolaborasi antar-tim berjalan efektif.'),
(11, '2023-Q4', 89, 'Proses rekrutmen dan orientasi talent berjalan mulus.'),
(13, '2023-Q4', 93, 'Analisis biaya cloud berhasil menghemat anggaran 15%.'),
(14, '2023-Q4', 86, 'Laporan audit keuangan kuartalan selesai sebelum tenggat.');
