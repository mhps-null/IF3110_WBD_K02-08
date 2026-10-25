USE test_inventory_mysql;

-- 1. Suppliers
CREATE TABLE suppliers (
    id INT AUTO_INCREMENT PRIMARY KEY,
    company_name VARCHAR(100) NOT NULL,
    contact_email VARCHAR(100) NOT NULL UNIQUE,
    country VARCHAR(50) DEFAULT 'Indonesia',
    is_active TINYINT(1) DEFAULT 1,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2. Warehouses
CREATE TABLE warehouses (
    id INT AUTO_INCREMENT PRIMARY KEY,
    code VARCHAR(20) NOT NULL UNIQUE,
    city VARCHAR(50) NOT NULL,
    capacity INT NOT NULL,
    CONSTRAINT chk_capacity CHECK (capacity > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 3. Inventory Items
CREATE TABLE inventory (
    id INT AUTO_INCREMENT PRIMARY KEY,
    supplier_id INT NOT NULL,
    warehouse_id INT NOT NULL,
    item_name VARCHAR(100) NOT NULL,
    item_code VARCHAR(50) NOT NULL UNIQUE,
    status ENUM('active', 'inactive', 'discontinued') DEFAULT 'active',
    quantity INT NOT NULL DEFAULT 0,
    unit_cost DECIMAL(10,2) NOT NULL,
    last_restocked TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_qty CHECK (quantity >= 0),
    CONSTRAINT chk_cost CHECK (unit_cost > 0),
    CONSTRAINT fk_inv_supplier FOREIGN KEY (supplier_id) REFERENCES suppliers(id) ON DELETE CASCADE,
    CONSTRAINT fk_inv_warehouse FOREIGN KEY (warehouse_id) REFERENCES warehouses(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE INDEX idx_inv_status ON inventory(status);
CREATE INDEX idx_inv_warehouse ON inventory(warehouse_id);

-- Seed Data: Suppliers
INSERT INTO suppliers (company_name, contact_email, country) VALUES
('PT Sumber Logistik Nusantara', 'info@sumberlogistik.co.id', 'Indonesia'),
('Tokyo Precision Components Ltd', 'contact@tokyoprecision.jp', 'Japan'),
('Shenzhen Global Electronics', 'sales@shenzhenglob.cn', 'China'),
('PT Mitra Sejahtera Bersama', 'admin@mitrasejahtera.id', 'Indonesia');

-- Seed Data: Warehouses
INSERT INTO warehouses (code, city, capacity) VALUES
('WH-JKT-01', 'Jakarta Utara', 15000),
('WH-SBY-01', 'Surabaya', 10000),
('WH-BDG-01', 'Bandung', 8000),
('WH-MDN-01', 'Medan', 6000);

-- Seed Data: Inventory (20 rows)
INSERT INTO inventory (supplier_id, warehouse_id, item_name, item_code, status, quantity, unit_cost) VALUES
(1, 1, 'Kardus Box Ukuran L (Corrugated)', 'BOX-COR-L', 'active', 5000, 7500.00),
(1, 1, 'Kardus Box Ukuran M (Corrugated)', 'BOX-COR-M', 'active', 7500, 5500.00),
(1, 2, 'Bubble Wrap Roll 1.25m x 50m', 'BBW-125-50', 'active', 250, 95000.00),
(1, 3, 'Lakban Cokelat Heavy Duty 48mm', 'TAPE-BRW-48', 'active', 1200, 12500.00),
(2, 1, 'Bantalan Presisi Miniatur 608ZZ', 'BEAR-608ZZ', 'active', 3000, 18500.00),
(2, 2, 'Bantalan Presisi Linear LM8UU', 'BEAR-LM8UU', 'active', 1500, 24000.00),
(2, 3, 'Stepper Motor NEMA 17 42mm', 'MTR-NEMA-17', 'active', 450, 115000.00),
(3, 1, 'Microcontroller ESP32-WROOM-32', 'MCU-ESP32-32', 'active', 2000, 48000.00),
(3, 2, 'OLED Display 0.96 Inch I2C', 'DSP-OLED-096', 'active', 850, 27500.00),
(3, 1, 'Relay Module 4-Channel 5V Optocoupler', 'MOD-RLY-4CH', 'active', 600, 32000.00),
(3, 3, 'Power Supply Switching 12V 10A', 'PSU-12V-10A', 'active', 180, 145000.00),
(4, 1, 'Plastik Shrink Wrap Roll 50cm', 'PL-SHR-50', 'active', 350, 82000.00),
(4, 2, 'Pallet Plastik Heavy Duty 120x100', 'PLT-HD-120', 'active', 120, 285000.00),
(2, 4, 'Sensor Suhu Digital DS18B20 Waterproof', 'SNS-DS18B20', 'active', 900, 21000.00),
(3, 4, 'Sensor Jarak Ultrasonik HC-SR04', 'SNS-HCSR04', 'active', 1100, 16000.00),
(1, 4, 'Tali Strapping Band 15mm 1000m', 'STRAP-15MM', 'active', 75, 165000.00),
(3, 2, 'Kabel Jumper Dupont 40 Pin M-F', 'CBL-DUP-MF', 'active', 800, 8500.00),
(2, 1, 'Lead Screw T8 8mm Panjang 300mm', 'SCW-T8-300', 'active', 220, 42000.00),
(3, 3, 'Driver Motor L298N Dual H-Bridge', 'DRV-L298N', 'inactive', 50, 25000.00),
(4, 3, 'Label Barcode Thermal 100x150mm Roll', 'LBL-THM-150', 'active', 400, 52000.00);
