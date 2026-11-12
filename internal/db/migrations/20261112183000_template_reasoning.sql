-- Add column "efforts" to table: "instance_templates"
ALTER TABLE `instance_templates` ADD COLUMN `efforts` text NOT NULL DEFAULT '';
-- Add column "reads_effort" to table: "instance_templates"
ALTER TABLE `instance_templates` ADD COLUMN `reads_effort` integer NOT NULL DEFAULT 0;
-- Add column "thinking" to table: "instance_templates"
ALTER TABLE `instance_templates` ADD COLUMN `thinking` text NOT NULL DEFAULT '';
-- Add column "thinking_switch" to table: "instance_templates"
ALTER TABLE `instance_templates` ADD COLUMN `thinking_switch` text NOT NULL DEFAULT '';
-- Add column "reasoning_error" to table: "instance_templates"
ALTER TABLE `instance_templates` ADD COLUMN `reasoning_error` text NOT NULL DEFAULT '';
