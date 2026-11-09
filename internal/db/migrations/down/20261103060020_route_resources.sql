-- Disable the enforcement of foreign-keys constraints
PRAGMA foreign_keys = off;
DROP TABLE `scheduler_configs`;
DROP TABLE `objectives`;
DROP TABLE `pools`;
-- Create "old_routes" table, the owner back in its instance or slot column, the state and profile out of the JSON
CREATE TABLE `old_routes` (
  `name` text NULL,
  `instance_id` text NOT NULL DEFAULT '',
  `slot_id` text NOT NULL DEFAULT '',
  `endpoint` text NOT NULL DEFAULT '',
  `api` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `model` text NOT NULL DEFAULT '',
  `requests` integer NOT NULL DEFAULT 0,
  `updated_at` text NOT NULL,
  `served` text NOT NULL DEFAULT '',
  `profile` text NOT NULL DEFAULT '',
  PRIMARY KEY (`name`)
);
INSERT INTO `old_routes` (`name`, `instance_id`, `slot_id`, `state`, `updated_at`, `profile`) SELECT `name`, CASE WHEN `owner_kind` = 'instance' THEN `owner_id` ELSE '' END, CASE WHEN `owner_kind` = 'slot' THEN `owner_id` ELSE '' END, CASE WHEN `status` != '' THEN COALESCE(lower(replace(json_extract(`status`, '$.state'), 'ROUTE_STATE_', '')), '') ELSE '' END, `updated_at`, CASE WHEN `spec` != '' THEN COALESCE(json_extract(`spec`, '$.profile'), '') ELSE '' END FROM `routes`;
DROP TABLE `routes`;
ALTER TABLE `old_routes` RENAME TO `routes`;
-- Enable back the enforcement of foreign-keys constraints
PRAGMA foreign_keys = on;
