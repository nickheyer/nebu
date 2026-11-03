-- Create "traces" table
CREATE TABLE `traces` (
  `id` text NULL,
  `owner` text NOT NULL,
  `route` text NOT NULL,
  `kind` integer NOT NULL,
  `started_at` text NOT NULL,
  `record` blob NOT NULL,
  `request` text NOT NULL,
  `upstream_request` text NOT NULL,
  `response` text NOT NULL,
  PRIMARY KEY (`id`)
);
-- Create index "traces_by_started" to table: "traces"
CREATE INDEX `traces_by_started` ON `traces` (`started_at`);
-- Create index "traces_by_owner" to table: "traces"
CREATE INDEX `traces_by_owner` ON `traces` (`owner`, `started_at`);
-- Create index "traces_by_route" to table: "traces"
CREATE INDEX `traces_by_route` ON `traces` (`route`, `started_at`);
