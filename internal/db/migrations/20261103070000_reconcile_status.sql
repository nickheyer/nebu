-- Add column "generation" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `generation` integer NOT NULL DEFAULT 0;
-- Add column "observed_generation" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `observed_generation` integer NOT NULL DEFAULT 0;
-- Add column "restarts" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `restarts` integer NOT NULL DEFAULT 0;
-- Add column "last_health_at" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `last_health_at` text NULL;
-- Add column "healthy" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `healthy` integer NOT NULL DEFAULT 0;
-- Add column "conditions" to table: "instances"
ALTER TABLE `instances` ADD COLUMN `conditions` text NOT NULL DEFAULT '';
-- Add column "route_policy" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `route_policy` text NOT NULL DEFAULT '';
-- Add column "route_profile" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `route_profile` text NOT NULL DEFAULT '';
-- Add column "idle_unload_seconds" to table: "instance_requests"
ALTER TABLE `instance_requests` ADD COLUMN `idle_unload_seconds` integer NOT NULL DEFAULT 0;
-- Add column "desired_running" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `desired_running` integer NOT NULL DEFAULT 0;
-- Add column "conditions" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `conditions` text NOT NULL DEFAULT '';
-- Add column "meta" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `meta` text NOT NULL DEFAULT '';
-- Add column "observed_generation" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `observed_generation` integer NOT NULL DEFAULT 0;
-- Add column "route_policy" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `route_policy` text NOT NULL DEFAULT '';
-- Add column "route_profile" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `route_profile` text NOT NULL DEFAULT '';
-- Add column "idle_unload_seconds" to table: "slot_requests"
ALTER TABLE `slot_requests` ADD COLUMN `idle_unload_seconds` integer NOT NULL DEFAULT 0;
-- Add column "generation" to table: "formations"
ALTER TABLE `formations` ADD COLUMN `generation` integer NOT NULL DEFAULT 0;
-- Add column "observed_generation" to table: "formations"
ALTER TABLE `formations` ADD COLUMN `observed_generation` integer NOT NULL DEFAULT 0;
-- Add column "conditions" to table: "formations"
ALTER TABLE `formations` ADD COLUMN `conditions` text NOT NULL DEFAULT '';
-- Add column "relaunches" to table: "formations"
ALTER TABLE `formations` ADD COLUMN `relaunches` integer NOT NULL DEFAULT 0;
