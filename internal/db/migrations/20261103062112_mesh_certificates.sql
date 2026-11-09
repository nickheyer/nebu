-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
-- Create "new_mesh" table
CREATE TABLE `new_mesh` (
  `id` text NULL,
  `name` text NOT NULL,
  `ca_certificate` blob NOT NULL DEFAULT (X''),
  `ca_key` blob NOT NULL DEFAULT (X''),
  `certificate` blob NOT NULL DEFAULT (X''),
  `key` blob NOT NULL DEFAULT (X''),
  `issuer` text NOT NULL DEFAULT '',
  `issuer_epoch` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `joined_at` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Leave meshes held under a shared secret, their members joining again with certificates
DELETE FROM `mesh_members`;
DELETE FROM `mesh_sessions`;
DELETE FROM `mesh_admissions`;
-- Drop "mesh" table
DROP TABLE `mesh`;
-- Rename temporary table "new_mesh" to "mesh"
ALTER TABLE `new_mesh` RENAME TO `mesh`;
-- Add column "join_grant" to table: "mesh_admissions"
ALTER TABLE `mesh_admissions` ADD COLUMN `join_grant` text NOT NULL DEFAULT '';
-- Create "mesh_revocations" table
CREATE TABLE `mesh_revocations` (
  `serial` text NULL,
  `node_id` text NOT NULL DEFAULT '',
  `not_after` text NOT NULL,
  `revoked_at` text NOT NULL,
  PRIMARY KEY (`serial`)
);
-- Create "mesh_grants" table
CREATE TABLE `mesh_grants` (
  `nonce` text NULL,
  `expires_at` text NOT NULL,
  `used_at` text NOT NULL,
  PRIMARY KEY (`nonce`)
);
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
