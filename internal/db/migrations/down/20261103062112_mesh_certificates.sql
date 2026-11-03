-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
DROP TABLE `mesh_grants`;
DROP TABLE `mesh_revocations`;
ALTER TABLE `mesh_admissions` DROP COLUMN `join_grant`;
-- Create "old_mesh" table under a shared secret, certificate meshes joining again
CREATE TABLE `old_mesh` (
  `id` text NULL,
  `name` text NOT NULL,
  `secret` blob NOT NULL,
  `tls` integer NOT NULL DEFAULT 0,
  `ca_certificate` blob NOT NULL DEFAULT (X''),
  `ca_key` blob NOT NULL DEFAULT (X''),
  `certificate` blob NOT NULL DEFAULT (X''),
  `created_at` text NOT NULL,
  `joined_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
DELETE FROM `mesh_members`;
DELETE FROM `mesh_sessions`;
DELETE FROM `mesh_admissions`;
DROP TABLE `mesh`;
ALTER TABLE `old_mesh` RENAME TO `mesh`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
