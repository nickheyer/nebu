-- Create "route_overrides" table
CREATE TABLE `route_overrides` (
  `name` text NULL,
  `spec` text NOT NULL DEFAULT '',
  `created_at` text NOT NULL,
  `updated_at` text NOT NULL,
  PRIMARY KEY (`name`)
);
