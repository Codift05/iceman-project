-- +goose Up

-- Lima peran pada PRD Bab 16. Ditandai is_system agar tidak dapat dihapus
-- lewat antarmuka aplikasi.
INSERT INTO roles (code, name, is_system) VALUES
    ('SUPER_ADMIN', 'Super Admin',          true),
    ('ADMIN_OPS',   'Admin Operasional',    true),
    ('FINANCE',     'Keuangan',             true),
    ('MANAGEMENT',  'Manajemen',            true),
    ('DRIVER',      'Driver',               true);

INSERT INTO permissions (code, resource, action) VALUES
    ('depot.view',             'depot',       'view'),
    ('depot.manage',           'depot',       'manage'),
    ('product.view',           'product',     'view'),
    ('product.manage',         'product',     'manage'),
    ('promo.view',             'promo',       'view'),
    ('promo.manage',           'promo',       'manage'),
    ('area_slot.view',         'area_slot',   'view'),
    ('area_slot.manage',       'area_slot',   'manage'),
    ('order.view',             'order',       'view'),
    ('order.manage',           'order',       'manage'),
    ('order.create_manual',    'order',       'create_manual'),
    ('payment.view',           'payment',     'view'),
    ('payment.manage',         'payment',     'manage'),
    ('refund.request',         'refund',      'request'),
    ('refund.process',         'refund',      'process'),
    ('refund.approve',         'refund',      'approve'),
    ('customer.view',          'customer',    'view'),
    ('customer.manage',        'customer',    'manage'),
    ('customer.view_finance',  'customer',    'view_finance'),
    ('customer.manage_terms',  'customer',    'manage_terms'),
    ('driver.view',            'driver',      'view'),
    ('driver.manage',          'driver',      'manage'),
    ('dispatch.manage',        'dispatch',    'manage'),
    ('delivery.view_own',      'delivery',    'view_own'),
    ('delivery.update_own',    'delivery',    'update_own'),
    ('tracking.view',          'tracking',    'view'),
    ('report.view_operational','report',      'view_operational'),
    ('report.view_financial',  'report',      'view_financial'),
    ('report.view_summary',    'report',      'view_summary'),
    ('report.export',          'report',      'export'),
    ('user.manage',            'user',        'manage'),
    ('role.manage',            'role',        'manage'),
    ('audit.view',             'audit',       'view'),
    ('settings.view',          'settings',    'view'),
    ('settings.manage',        'settings',    'manage');

-- Super Admin memperoleh seluruh izin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE  r.code = 'SUPER_ADMIN';

-- Admin Operasional: seluruh operasional harian, tanpa pengelolaan pengguna
-- dan tanpa pemrosesan refund.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r JOIN permissions p ON p.code IN (
    'depot.view', 'product.view', 'product.manage', 'promo.view', 'promo.manage',
    'area_slot.view', 'area_slot.manage', 'order.view', 'order.manage',
    'order.create_manual', 'payment.view', 'refund.request',
    'customer.view', 'customer.manage', 'driver.view', 'driver.manage',
    'dispatch.manage', 'tracking.view', 'report.view_operational',
    'report.export', 'settings.view')
WHERE r.code = 'ADMIN_OPS';

-- Keuangan: pembayaran dan refund, tanpa master data operasional.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r JOIN permissions p ON p.code IN (
    'depot.view', 'order.view', 'payment.view', 'payment.manage',
    'refund.process', 'customer.view_finance', 'report.view_financial',
    'report.export', 'settings.view')
WHERE r.code = 'FINANCE';

-- Manajemen: hanya membaca.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r JOIN permissions p ON p.code IN (
    'depot.view', 'product.view', 'promo.view', 'area_slot.view', 'order.view',
    'payment.view', 'customer.view', 'driver.view', 'tracking.view',
    'report.view_operational', 'report.view_financial', 'report.view_summary',
    'audit.view', 'settings.view')
WHERE r.code = 'MANAGEMENT';

-- Driver: hanya tugas yang ditugaskan kepadanya.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r JOIN permissions p ON p.code IN (
    'delivery.view_own', 'delivery.update_own')
WHERE r.code = 'DRIVER';

-- +goose Down
DELETE FROM role_permissions;
DELETE FROM permissions;
DELETE FROM roles;
