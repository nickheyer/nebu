-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
DROP TABLE `formation_candidates`;
DROP TABLE `formation_seats`;
DROP TABLE `formations`;
DROP TABLE `device_profiles`;
DROP TABLE `formation_ratios`;
DROP TABLE `throughput`;
DROP TABLE `mesh_links`;
DROP TABLE `mesh_sessions`;
DROP TABLE `mesh_admissions`;
DROP TABLE `mesh_members`;
DROP TABLE `mesh`;
DROP TABLE `mesh_identity`;
ALTER TABLE `slot_requests` DROP COLUMN `profile`;
ALTER TABLE `slot_requests` DROP COLUMN `shape`;
ALTER TABLE `slot_requests` DROP COLUMN `span`;
ALTER TABLE `slots` DROP COLUMN `formation_id`;
ALTER TABLE `instance_requests` DROP COLUMN `profile`;
ALTER TABLE `instance_requests` DROP COLUMN `seat`;
ALTER TABLE `instance_requests` DROP COLUMN `shape`;
ALTER TABLE `instance_requests` DROP COLUMN `span`;
ALTER TABLE `instances` DROP COLUMN `transport`;
ALTER TABLE `instances` DROP COLUMN `seat`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
