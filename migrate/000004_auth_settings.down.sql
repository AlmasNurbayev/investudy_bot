-- Обратное сиду: убираются только строки, которые завела эта миграция.
DELETE FROM settings WHERE key IN ('auth', 'pnl');
