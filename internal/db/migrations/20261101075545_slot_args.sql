-- Add column "args" to table: "slots"
ALTER TABLE `slots` ADD COLUMN `args` text NOT NULL DEFAULT '';
