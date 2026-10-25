-- Connect to source test database
\connect test_ecommerce_pg;

-- Create analytics schema (for multi-schema introspection testing)
CREATE SCHEMA IF NOT EXISTS analytics;

-- 1. Table Customers
CREATE TABLE customers (
    id SERIAL PRIMARY KEY,
    full_name VARCHAR(100) NOT NULL,
    email VARCHAR(150) UNIQUE NOT NULL,
    phone VARCHAR(30),
    is_vip BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 2. Table Products
CREATE TABLE products (
    id SERIAL PRIMARY KEY,
    sku VARCHAR(50) UNIQUE NOT NULL,
    title TEXT NOT NULL,
    price NUMERIC(12,2) NOT NULL CHECK (price > 0),
    stock INTEGER NOT NULL CHECK (stock >= 0),
    category VARCHAR(50)
);

-- 3. Table Orders
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    order_number VARCHAR(30) UNIQUE NOT NULL,
    total_amount NUMERIC(12,2) NOT NULL CHECK (total_amount >= 0),
    status VARCHAR(30) NOT NULL DEFAULT 'pending',
    order_date TIMESTAMPTZ DEFAULT NOW()
);

-- 4. Table Order Items
CREATE TABLE order_items (
    id SERIAL PRIMARY KEY,
    order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id INTEGER NOT NULL REFERENCES products(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC(12,2) NOT NULL CHECK (unit_price > 0)
);

-- 5. Table in analytics schema
CREATE TABLE analytics.user_metrics (
    id SERIAL PRIMARY KEY,
    customer_id INTEGER NOT NULL,
    total_spend NUMERIC(14,2) DEFAULT 0.00,
    last_active TIMESTAMPTZ DEFAULT NOW()
);

-- Indexes
CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_orders_completed ON orders(order_date) WHERE status = 'completed';
CREATE INDEX idx_products_category ON products(category);

-- Seed Data: Customers (25 rows)
INSERT INTO customers (full_name, email, phone, is_vip) VALUES
('Budi Santoso', 'budi@example.com', '081234567890', true),
('Siti Rahma', 'siti@example.com', '081234567891', false),
('Ahmad Fauzi', 'ahmad@example.com', '081234567892', true),
('Dewi Lestari', 'dewi@example.com', '081234567893', false),
('Eko Prasetyo', 'eko@example.com', '081234567894', false),
('Fitri Handayani', 'fitri@example.com', '081234567895', true),
('Gita Gutawa', 'gita@example.com', '081234567896', false),
('Hadi Wijaya', 'hadi@example.com', '081234567897', false),
('Indah Permata', 'indah@example.com', '081234567898', true),
('Joko Widodo', 'joko@example.com', '081234567899', false),
('Kartika Putri', 'kartika@example.com', '081234567800', false),
('Lukman Hakim', 'lukman@example.com', '081234567801', false),
('Mega Utami', 'mega@example.com', '081234567802', true),
('Naufal Rizky', 'naufal@example.com', '081234567803', false),
('Olivia Zalianty', 'olivia@example.com', '081234567804', false),
('Prasetyo Adi', 'adi@example.com', '081234567805', true),
('Qori Sandioriva', 'qori@example.com', '081234567806', false),
('Rian D’Masiv', 'rian@example.com', '081234567807', false),
('Sule Prikitiw', 'sule@example.com', '081234567808', true),
('Taufik Hidayat', 'taufik@example.com', '081234567809', false),
('Umar Bakri', 'umar@example.com', '081234567810', false),
('Vina Panduwinata', 'vina@example.com', '081234567811', true),
('Wawan Hendrawan', 'wawan@example.com', '081234567812', false),
('Xena Warrior', 'xena@example.com', '081234567813', false),
('Yuni Shara', 'yuni@example.com', '081234567814', true);

-- Seed Data: Products (15 rows)
INSERT INTO products (sku, title, price, stock, category) VALUES
('SKU-LAP-01', 'ThinkPad X1 Carbon Gen 11', 28500000.00, 15, 'Laptop'),
('SKU-LAP-02', 'MacBook Air M2 16GB', 18999000.00, 25, 'Laptop'),
('SKU-MOU-01', 'Logitech MX Master 3S', 1599000.00, 50, 'Aksesoris'),
('SKU-KEY-01', 'Keychron K2 Pro Mechanical', 1450000.00, 40, 'Aksesoris'),
('SKU-MON-01', 'Dell UltraSharp 27 4K U2723QE', 8900000.00, 12, 'Monitor'),
('SKU-MON-02', 'LG 24 Inch Full HD IPS', 1650000.00, 30, 'Monitor'),
('SKU-AUD-01', 'Sony WH-1000XM5 ANC Headphone', 4999000.00, 20, 'Audio'),
('SKU-AUD-02', 'AirPods Pro 2 USB-C', 3899000.00, 35, 'Audio'),
('SKU-TAB-01', 'iPad Pro 11 M4 OLED', 16999000.00, 10, 'Tablet'),
('SKU-PHO-01', 'iPhone 15 Pro Max 256GB', 22499000.00, 18, 'Smartphone'),
('SKU-PHO-02', 'Samsung Galaxy S24 Ultra', 21999000.00, 15, 'Smartphone'),
('SKU-CAB-01', 'Anker PowerLine III USB-C 100W', 150000.00, 100, 'Kabel'),
('SKU-CHA-01', 'UGREEN 100W GaN Fast Charger', 550000.00, 80, 'Charger'),
('SKU-STO-01', 'Samsung T7 Shield SSD 1TB', 1750000.00, 45, 'Storage'),
('SKU-DES-01', 'Standing Desk Elektrik Dual Motor', 3900000.00, 8, 'Furnitur');

-- Seed Data: Orders (15 rows)
INSERT INTO orders (customer_id, order_number, total_amount, status) VALUES
(1, 'ORD-2026-0001', 30099000.00, 'completed'),
(2, 'ORD-2026-0002', 1599000.00, 'completed'),
(3, 'ORD-2026-0003', 18999000.00, 'processing'),
(4, 'ORD-2026-0004', 3899000.00, 'pending'),
(5, 'ORD-2026-0005', 10550000.00, 'completed'),
(6, 'ORD-2026-0006', 4999000.00, 'completed'),
(7, 'ORD-2026-0007', 150000.00, 'cancelled'),
(8, 'ORD-2026-0008', 22499000.00, 'processing'),
(9, 'ORD-2026-0009', 1450000.00, 'completed'),
(10, 'ORD-2026-0010', 550000.00, 'pending'),
(11, 'ORD-2026-0011', 28500000.00, 'completed'),
(12, 'ORD-2026-0012', 3900000.00, 'completed'),
(13, 'ORD-2026-0013', 1750000.00, 'processing'),
(14, 'ORD-2026-0014', 1650000.00, 'completed'),
(15, 'ORD-2026-0015', 21999000.00, 'pending');

-- Seed Data: Order Items
INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES
(1, 1, 1, 28500000.00),
(1, 3, 1, 1599000.00),
(2, 3, 1, 1599000.00),
(3, 2, 1, 18999000.00),
(4, 8, 1, 3899000.00),
(5, 5, 1, 8900000.00),
(5, 6, 1, 1650000.00),
(6, 7, 1, 4999000.00),
(7, 12, 1, 150000.00),
(8, 10, 1, 22499000.00),
(9, 4, 1, 1450000.00),
(10, 13, 1, 550000.00),
(11, 1, 1, 28500000.00),
(12, 15, 1, 3900000.00),
(13, 14, 1, 1750000.00),
(14, 6, 1, 1650000.00),
(15, 11, 1, 21999000.00);

-- Seed Data: Analytics
INSERT INTO analytics.user_metrics (customer_id, total_spend) VALUES
(1, 30099000.00),
(2, 1599000.00),
(3, 18999000.00),
(5, 10550000.00),
(6, 4999000.00);
