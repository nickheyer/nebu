-- Create "installs" table
CREATE TABLE `installs` (
  `id` text NULL,
  `runtime_id` text NOT NULL,
  `kind` text NOT NULL,
  `path` text NOT NULL,
  `dir` text NOT NULL,
  `version` text NOT NULL DEFAULT '',
  `origin` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `build_id` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Create index "installs_by_runtime" to table: "installs"
CREATE INDEX `installs_by_runtime` ON `installs` (`runtime_id`, `created_at`);
-- Create "install_facts" table
CREATE TABLE `install_facts` (
  `install_id` text NOT NULL,
  `key` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`install_id`, `key`),
  CONSTRAINT `0` FOREIGN KEY (`install_id`) REFERENCES `installs` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "calibrations" table
CREATE TABLE `calibrations` (
  `runtime_id` text NOT NULL,
  `architecture` text NOT NULL,
  `overhead_delta` real NOT NULL,
  `samples` integer NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`runtime_id`, `architecture`)
);
-- Create "instances" table
CREATE TABLE `instances` (
  `id` text NULL,
  `name` text NOT NULL,
  `source_id` text NOT NULL,
  `repo` text NOT NULL,
  `weight_group` text NOT NULL,
  `runtime_id` text NOT NULL,
  `install_id` text NOT NULL,
  `endpoint` text NOT NULL,
  `state` text NOT NULL,
  `pid` integer NOT NULL DEFAULT 0,
  `error` text NOT NULL DEFAULT '',
  `task_id` text NOT NULL DEFAULT '',
  `desired_running` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `ready_at` text NULL,
  `stopped_at` text NULL,
  `slot_id` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Create index "instances_by_created" to table: "instances"
CREATE INDEX `instances_by_created` ON `instances` (`created_at`);
-- Create "instance_params" table
CREATE TABLE `instance_params` (
  `instance_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`instance_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_command" table
CREATE TABLE `instance_command` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `arg` text NOT NULL,
  PRIMARY KEY (`instance_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_requests" table
CREATE TABLE `instance_requests` (
  `instance_id` text NULL,
  `source_id` text NOT NULL,
  `repo` text NOT NULL,
  `weight_group` text NOT NULL,
  `runtime_id` text NOT NULL,
  `install_id` text NOT NULL,
  `name` text NOT NULL,
  `slot_id` text NOT NULL DEFAULT '',
  `force` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_request_params" table
CREATE TABLE `instance_request_params` (
  `instance_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`instance_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_plans" table
CREATE TABLE `instance_plans` (
  `instance_id` text NULL,
  `verdict` text NOT NULL,
  `weights_bytes` integer NOT NULL,
  `cache_bytes` integer NOT NULL,
  `overhead_bytes` integer NOT NULL,
  `detail` text NOT NULL DEFAULT '',
  `overhead_delta` real NOT NULL DEFAULT 0,
  PRIMARY KEY (`instance_id`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_plan_pools" table
CREATE TABLE `instance_plan_pools` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `pool_id` text NOT NULL,
  `kind` text NOT NULL,
  `used_bytes` integer NOT NULL,
  `capacity_bytes` integer NOT NULL,
  PRIMARY KEY (`instance_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_plan_placements" table
CREATE TABLE `instance_plan_placements` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `kind` text NOT NULL,
  `pool_id` text NOT NULL,
  `bytes` integer NOT NULL,
  `count` integer NOT NULL,
  PRIMARY KEY (`instance_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_plan_params" table
CREATE TABLE `instance_plan_params` (
  `instance_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`instance_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_measurements" table
CREATE TABLE `instance_measurements` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `key` text NOT NULL,
  `bytes` integer NOT NULL,
  `line` text NOT NULL DEFAULT '',
  PRIMARY KEY (`instance_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_triage" table
CREATE TABLE `instance_triage` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `rule_id` text NOT NULL,
  `summary` text NOT NULL,
  `hint` text NOT NULL,
  `line` text NOT NULL,
  PRIMARY KEY (`instance_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "instance_triage_fixes" table
CREATE TABLE `instance_triage_fixes` (
  `instance_id` text NOT NULL,
  `position` integer NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`instance_id`, `position`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`instance_id`) REFERENCES `instances` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "tasks" table
CREATE TABLE `tasks` (
  `id` text NULL,
  `kind` text NOT NULL,
  `title` text NOT NULL,
  `state` text NOT NULL,
  `progress_done` integer NOT NULL DEFAULT 0,
  `progress_total` integer NOT NULL DEFAULT 0,
  `progress_message` text NOT NULL DEFAULT '',
  `error` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `started_at` text NULL,
  `finished_at` text NULL,
  PRIMARY KEY (`id`)
);
-- Create index "tasks_by_created" to table: "tasks"
CREATE INDEX `tasks_by_created` ON `tasks` (`created_at`);
-- Create "task_labels" table
CREATE TABLE `task_labels` (
  `task_id` text NOT NULL,
  `key` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`task_id`, `key`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "task_logs" table
CREATE TABLE `task_logs` (
  `task_id` text NOT NULL,
  `position` integer NOT NULL,
  `line` text NOT NULL,
  PRIMARY KEY (`task_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "builds" table
CREATE TABLE `builds` (
  `id` text NULL,
  `recipe_id` text NOT NULL,
  `runtime_id` text NOT NULL,
  `variant` text NOT NULL,
  `ref` text NOT NULL DEFAULT '',
  `commit_id` text NOT NULL DEFAULT '',
  `sandbox` text NOT NULL,
  `image` text NOT NULL DEFAULT '',
  `dir` text NOT NULL,
  `binary` text NOT NULL DEFAULT '',
  `install_id` text NOT NULL DEFAULT '',
  `task_id` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `error` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `finished_at` text NULL,
  PRIMARY KEY (`id`)
);
-- Create index "builds_by_runtime" to table: "builds"
CREATE INDEX `builds_by_runtime` ON `builds` (`runtime_id`, `created_at`);
-- Create "build_vars" table
CREATE TABLE `build_vars` (
  `build_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`build_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`build_id`) REFERENCES `builds` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "build_facts" table
CREATE TABLE `build_facts` (
  `build_id` text NOT NULL,
  `key` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`build_id`, `key`),
  CONSTRAINT `0` FOREIGN KEY (`build_id`) REFERENCES `builds` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "build_patches" table
CREATE TABLE `build_patches` (
  `build_id` text NOT NULL,
  `position` integer NOT NULL,
  `patch_id` text NOT NULL,
  PRIMARY KEY (`build_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`build_id`) REFERENCES `builds` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "slots" table
CREATE TABLE `slots` (
  `id` text NULL,
  `name` text NOT NULL,
  `description` text NOT NULL DEFAULT '',
  `runtime_id` text NOT NULL DEFAULT '',
  `memory_bytes` integer NOT NULL DEFAULT 0,
  `instance_id` text NOT NULL DEFAULT '',
  `state` text NOT NULL,
  `error` text NOT NULL DEFAULT '',
  `task_id` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  `max_in_flight` integer NOT NULL DEFAULT 0,
  `requests_per_second` real NOT NULL DEFAULT 0,
  `burst` integer NOT NULL DEFAULT 0,
  `request_timeout_ms` integer NOT NULL DEFAULT 0,
  `upstream_timeout_ms` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`)
);
-- Create index "slots_name" to table: "slots"
CREATE UNIQUE INDEX `slots_name` ON `slots` (`name`);
-- Create "slot_devices" table
CREATE TABLE `slot_devices` (
  `slot_id` text NOT NULL,
  `position` integer NOT NULL,
  `device_id` text NOT NULL,
  PRIMARY KEY (`slot_id`, `position`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "slot_params" table
CREATE TABLE `slot_params` (
  `slot_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`slot_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "slot_requests" table
CREATE TABLE `slot_requests` (
  `slot_id` text NULL,
  `source_id` text NOT NULL,
  `repo` text NOT NULL,
  `weight_group` text NOT NULL,
  `runtime_id` text NOT NULL,
  `install_id` text NOT NULL,
  `name` text NOT NULL,
  `force` integer NOT NULL DEFAULT 0,
  PRIMARY KEY (`slot_id`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "slot_request_params" table
CREATE TABLE `slot_request_params` (
  `slot_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`slot_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`slot_id`) REFERENCES `slots` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "routes" table
CREATE TABLE `routes` (
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
  PRIMARY KEY (`name`)
);
-- Create "sources" table
CREATE TABLE `sources` (
  `id` text NULL,
  `kind` text NOT NULL,
  `seeded` integer NOT NULL DEFAULT 0,
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  `name` text NOT NULL DEFAULT '',
  PRIMARY KEY (`id`)
);
-- Create "source_config" table
CREATE TABLE `source_config` (
  `source_id` text NOT NULL,
  `name` text NOT NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`source_id`, `name`),
  CONSTRAINT `0` FOREIGN KEY (`source_id`) REFERENCES `sources` (`id`) ON UPDATE NO ACTION ON DELETE CASCADE
);
-- Create "settings" table
CREATE TABLE `settings` (
  `key` text NULL,
  `value` text NOT NULL,
  PRIMARY KEY (`key`)
);
