DROP TABLE IF EXISTS pnl_item_map;
DROP TABLE IF EXISTS pnl_lines;
DROP TABLE IF EXISTS sessions;

COMMENT ON COLUMN sub_items.item_id IS NULL;

-- users возвращается к форме 000002 на месте: в ней выданные доступы.
-- Пользователи только сайта (telegram_id NULL) в старую форму не помещаются —
-- откат на них упадёт при восстановлении ключа, а не удалит их молча.
ALTER TABLE users DROP COLUMN updated_at;
ALTER TABLE users DROP COLUMN blocked_at;
ALTER TABLE users DROP COLUMN is_admin;
ALTER TABLE users DROP COLUMN division_id;
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'viewer';
ALTER TABLE users DROP COLUMN password_hash;
ALTER TABLE users DROP COLUMN login;
ALTER TABLE users DROP CONSTRAINT users_telegram_id_key;
ALTER TABLE users DROP COLUMN id;
ALTER TABLE users ADD PRIMARY KEY (telegram_id);
