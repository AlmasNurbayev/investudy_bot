DROP VIEW IF EXISTS data_current;

-- data уходит раньше snapshots и справочников: на них ссылаются её FK.
DROP TABLE IF EXISTS data;
DROP TABLE IF EXISTS snapshots;

DROP TABLE IF EXISTS sub_items;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS divisions;
DROP TABLE IF EXISTS fin_types;
DROP TABLE IF EXISTS vids;
