-- Add column "install_id" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `install_id` text NOT NULL DEFAULT '';
