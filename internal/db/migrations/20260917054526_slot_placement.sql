-- Add column "placement" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `placement` text NOT NULL DEFAULT '';
